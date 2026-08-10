package main

import (
	"context"
	"encoding/json"
	"time"
)

// Fila de reentrega durável para as entradas destinadas ao Chatwoot.
//
// Motivação: a entrega WhatsApp -> Chatwoot é um POST síncrono. Se o Chatwoot
// está fora do ar (deploy, restart, queda momentânea) no instante em que a
// mensagem chega, o POST falha e — sem esta fila — a mensagem se perde. Aqui a
// entrega que falha é persistida no banco PRINCIPAL e reentregue por um worker
// de fundo com backoff exponencial. Como toda entrega usa `source_id` = ID da
// msg do WhatsApp, o Chatwoot deduplica a reentrega e nunca duplica a mensagem.
//
// A fila guarda só METADADOS (o binário de mídia não é persistido — é re-baixado
// do WhatsApp na hora da reentrega), então ela fica pequena e some do banco assim
// que o Chatwoot volta.

const (
	cwRetryBase   = 60 * time.Second // 1ª reentrega ~60s depois; dobra a cada tentativa
	cwRetryMax    = 30 * time.Minute // teto do backoff
	cwMaxAttempts = 18               // após isso vira "dead-letter" (fica logado, para de tentar)
	cwWorkerTick  = 15 * time.Second // frequência de varredura da fila
	cwBatchSize   = 50               // pendências processadas por varredura
)

func nowMillis() int64 { return time.Now().UnixMilli() }

// outboxBackoff devolve o atraso da próxima tentativa: 60s, 120s, 240s... até o
// teto de 30min.
func outboxBackoff(attempts int) time.Duration {
	d := cwRetryBase
	for i := 1; i < attempts; i++ {
		d *= 2
		if d >= cwRetryMax {
			return cwRetryMax
		}
	}
	return d
}

type outboxRow struct {
	ID        int64
	SessionID string
	Payload   []byte
	Attempts  int
}

// enqueueOutbox insere (ou revive) uma pendência. Idempotente por
// (session_id, source_id): um evento reprocessado atualiza a linha em vez de
// duplicá-la.
func (s *sessionStore) enqueueOutbox(ctx context.Context, sessionID, sourceID string, payload []byte, nextAt, createdAt int64) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO chatwoot_outbox (session_id, source_id, payload, attempts, next_at, created_at, dead)
		VALUES ($1, $2, $3, 0, $4, $5, false)
		ON CONFLICT (session_id, source_id) DO UPDATE
			SET payload = EXCLUDED.payload, next_at = EXCLUDED.next_at,
			    attempts = 0, dead = false, last_error = NULL`,
		sessionID, sourceID, payload, nextAt, createdAt)
	return err
}

// dueOutbox devolve as pendências vencidas (não mortas) prontas para reentrega.
func (s *sessionStore) dueOutbox(ctx context.Context, now int64, limit int) ([]outboxRow, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, session_id, payload, attempts
		FROM chatwoot_outbox
		WHERE dead = false AND next_at <= $1
		ORDER BY next_at
		LIMIT $2`, now, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []outboxRow{}
	for rows.Next() {
		var r outboxRow
		if err := rows.Scan(&r.ID, &r.SessionID, &r.Payload, &r.Attempts); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// rescheduleOutbox atualiza a pendência para uma nova tentativa futura.
func (s *sessionStore) rescheduleOutbox(ctx context.Context, id int64, attempts int, nextAt int64, lastErr string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE chatwoot_outbox SET attempts = $1, next_at = $2, last_error = $3 WHERE id = $4`,
		attempts, nextAt, lastErr, id)
	return err
}

// killOutbox marca a pendência como dead-letter (esgotou as tentativas).
func (s *sessionStore) killOutbox(ctx context.Context, id int64, lastErr string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE chatwoot_outbox SET dead = true, last_error = $1 WHERE id = $2`, lastErr, id)
	return err
}

func (s *sessionStore) deleteOutbox(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM chatwoot_outbox WHERE id = $1`, id)
	return err
}

// deleteOutboxForSession limpa as pendências órfãs quando a sessão é removida (a
// fila vive no banco principal, então não some junto com o banco da sessão).
func (s *sessionStore) deleteOutboxForSession(ctx context.Context, sessionID string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM chatwoot_outbox WHERE session_id = $1`, sessionID)
	return err
}

// runChatwootOutbox roda o worker de reentrega até o contexto encerrar.
func (m *SessionManager) runChatwootOutbox(ctx context.Context) {
	t := time.NewTicker(cwWorkerTick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			m.drainChatwootOutbox(ctx)
		}
	}
}

func (m *SessionManager) drainChatwootOutbox(ctx context.Context) {
	if m.store == nil {
		return
	}
	rows, err := m.store.dueOutbox(ctx, nowMillis(), cwBatchSize)
	if err != nil {
		m.log.Error("chatwoot outbox: consultar pendências falhou", "err", err)
		return
	}
	for _, row := range rows {
		m.processOutboxRow(ctx, row)
	}
}

func (m *SessionManager) processOutboxRow(ctx context.Context, row outboxRow) {
	sess, ok := m.Get(row.SessionID)
	if !ok {
		// sessão não está viva agora (reiniciando/reconectando): tenta de novo mais
		// tarde sem gastar tentativa, para não estourar o dead-letter à toa.
		_ = m.store.rescheduleOutbox(ctx, row.ID, row.Attempts,
			nowMillis()+cwRetryBase.Milliseconds(), "sessão offline")
		return
	}
	cfg := sess.getChatwoot()
	if !cfg.valid() {
		_ = m.store.rescheduleOutbox(ctx, row.ID, row.Attempts,
			nowMillis()+cwRetryMax.Milliseconds(), "chatwoot não configurado")
		return
	}
	var j cwJob
	if err := json.Unmarshal(row.Payload, &j); err != nil {
		m.log.Error("chatwoot outbox: payload inválido, descartando", "id", row.ID, "err", err)
		_ = m.store.deleteOutbox(ctx, row.ID)
		return
	}
	if err := sess.execChatwootJob(cfg, j); err != nil {
		attempts := row.Attempts + 1
		if attempts >= cwMaxAttempts {
			m.log.Error("chatwoot outbox: dead-letter após esgotar tentativas",
				"id", row.ID, "source", j.SourceID, "attempts", attempts, "err", err)
			_ = m.store.killOutbox(ctx, row.ID, err.Error())
			return
		}
		delay := outboxBackoff(attempts)
		m.log.Warn("chatwoot outbox: reentrega falhou, reagendando",
			"id", row.ID, "source", j.SourceID, "attempt", attempts, "delay", delay.String(), "err", err)
		_ = m.store.rescheduleOutbox(ctx, row.ID, attempts,
			nowMillis()+delay.Milliseconds(), err.Error())
		return
	}
	_ = m.store.deleteOutbox(ctx, row.ID)
	m.log.Info("chatwoot outbox: reentrega concluída", "id", row.ID, "source", j.SourceID)
}

// enqueueChatwoot serializa e persiste um job que falhou na entrega imediata.
func (s *Session) enqueueChatwoot(j cwJob) {
	if s.mgr == nil || s.mgr.store == nil {
		return
	}
	payload, err := json.Marshal(j)
	if err != nil {
		s.log.Error("chatwoot: serializar job da fila falhou", "err", err)
		return
	}
	now := nowMillis()
	if err := s.mgr.store.enqueueOutbox(s.mgr.appCtx, s.id, j.SourceID, payload,
		now+cwRetryBase.Milliseconds(), now); err != nil {
		s.log.Error("chatwoot: enfileirar reentrega falhou", "err", err)
	}
}

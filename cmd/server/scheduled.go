package main

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// Agendamento de envio (send_at). Ideia inspirada no waxum. A mensagem é montada
// no ATO do agendamento (mídia já é subida) e serializada em protojson; um worker
// único varre as pendências vencidas e envia via sendAndMark. Persiste no banco
// principal (sobrevive a restart).

const scheduledTick = 10 * time.Second

type scheduledRow struct {
	ID        int64           `json:"id"`
	SessionID string          `json:"sessionId"`
	ToJID     string          `json:"to"`
	Msg       json.RawMessage `json:"-"`
	SendAt    int64           `json:"sendAt"`
	Status    string          `json:"status"`
	LastError string          `json:"lastError,omitempty"`
	CreatedAt int64           `json:"createdAt"`
}

func (s *sessionStore) enqueueScheduled(ctx context.Context, sessionID, toJID string, msg []byte, sendAt, createdAt int64) (int64, error) {
	var id int64
	err := s.db.QueryRowContext(ctx,
		`INSERT INTO scheduled_messages (session_id, to_jid, msg, send_at, created_at) VALUES ($1,$2,$3,$4,$5) RETURNING id`,
		sessionID, toJID, msg, sendAt, createdAt).Scan(&id)
	return id, err
}

func (s *sessionStore) dueScheduled(ctx context.Context, now int64, limit int) ([]scheduledRow, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, session_id, to_jid, msg, send_at, status, COALESCE(last_error,''), created_at
		 FROM scheduled_messages WHERE status = 'pending' AND send_at <= $1 ORDER BY send_at LIMIT $2`, now, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanScheduled(rows, true)
}

func (s *sessionStore) listScheduled(ctx context.Context, sessionID string, limit int) ([]scheduledRow, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, session_id, to_jid, msg, send_at, status, COALESCE(last_error,''), created_at
		 FROM scheduled_messages WHERE session_id = $1 ORDER BY send_at DESC LIMIT $2`, sessionID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanScheduled(rows, false)
}

func (s *sessionStore) markScheduled(ctx context.Context, id int64, status, lastErr string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE scheduled_messages SET status = $1, last_error = $2 WHERE id = $3`, status, lastErr, id)
	return err
}

// cancelScheduled remove uma pendência (só se ainda estiver pending). Devolve se removeu.
func (s *sessionStore) cancelScheduled(ctx context.Context, sessionID string, id int64) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM scheduled_messages WHERE id = $1 AND session_id = $2 AND status = 'pending'`, id, sessionID)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

func scanScheduled(rows interface {
	Next() bool
	Scan(...any) error
}, withMsg bool) ([]scheduledRow, error) {
	var out []scheduledRow
	for rows.Next() {
		var r scheduledRow
		var msg []byte
		if err := rows.Scan(&r.ID, &r.SessionID, &r.ToJID, &msg, &r.SendAt, &r.Status, &r.LastError, &r.CreatedAt); err != nil {
			return nil, err
		}
		if withMsg {
			r.Msg = json.RawMessage(msg)
		}
		out = append(out, r)
	}
	return out, nil
}

// runScheduler é o worker único que envia as mensagens agendadas vencidas.
func (m *SessionManager) runScheduler(ctx context.Context) {
	t := time.NewTicker(scheduledTick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			m.drainScheduled(ctx)
		}
	}
}

func (m *SessionManager) drainScheduled(ctx context.Context) {
	if m.store == nil {
		return
	}
	rows, err := m.store.dueScheduled(ctx, nowMillis(), 50)
	if err != nil {
		m.log.Error("scheduler: consultar pendências falhou", "err", err)
		return
	}
	for _, row := range rows {
		m.sendScheduled(ctx, row)
	}
}

func (m *SessionManager) sendScheduled(ctx context.Context, row scheduledRow) {
	sess, ok := m.Get(row.SessionID)
	if !ok {
		// sessão offline agora: adia ~1min sem marcar como falha.
		_ = m.store.markScheduledFuture(ctx, row.ID, nowMillis()+60_000)
		return
	}
	var msg waE2E.Message
	if err := protojson.Unmarshal(row.Msg, &msg); err != nil {
		m.log.Error("scheduler: msg inválida, descartando", "id", row.ID, "err", err)
		_ = m.store.markScheduled(ctx, row.ID, "failed", "payload inválido")
		return
	}
	jid, err := resolveRecipient(row.ToJID)
	if err != nil {
		_ = m.store.markScheduled(ctx, row.ID, "failed", err.Error())
		return
	}
	sctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	_, err = sess.sendAndMark(sctx, jid, &msg)
	cancel()
	if err != nil {
		m.log.Warn("scheduler: envio falhou", "id", row.ID, "err", err)
		_ = m.store.markScheduled(ctx, row.ID, "failed", err.Error())
		return
	}
	_ = m.store.markScheduled(ctx, row.ID, "sent", "")
	m.log.Info("scheduler: mensagem agendada enviada", "id", row.ID, "to", row.ToJID)
}

// markScheduledFuture adia a hora de envio sem mudar o status (sessão offline).
func (s *sessionStore) markScheduledFuture(ctx context.Context, id, sendAt int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE scheduled_messages SET send_at = $1 WHERE id = $2 AND status = 'pending'`, sendAt, id)
	return err
}

// ---------- endpoints ----------

// POST /api/sessions/{sid}/schedule {to, sendAt, text | imageUrl|base64 + caption}
// sendAt em epoch ms (ou s; convertemos). Envia no futuro.
func (s *server) handleSchedule(w http.ResponseWriter, r *http.Request) {
	sess := s.pairedSession(w, r.PathValue("sid"))
	if sess == nil {
		return
	}
	var b struct {
		To       string `json:"to"`
		Text     string `json:"text"`
		ImageURL string `json:"imageUrl"`
		Base64   string `json:"base64"`
		Caption  string `json:"caption"`
		SendAt   int64  `json:"sendAt"`
	}
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil || strings.TrimSpace(b.To) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "to obrigatório"})
		return
	}
	jid, err := resolveRecipient(b.To)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	sendAt := normalizeEpochMillis(b.SendAt)
	if sendAt <= nowMillis() {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "sendAt deve ser no futuro (epoch ms)"})
		return
	}
	hasMedia := strings.TrimSpace(b.ImageURL) != "" || strings.TrimSpace(b.Base64) != ""
	if strings.TrimSpace(b.Text) == "" && !hasMedia {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "text ou imagem obrigatório"})
		return
	}
	var msg *waE2E.Message
	if hasMedia {
		up, ok := s.uploadMedia(sess, w, r, b.Base64, b.ImageURL, whatsmeow.MediaImage)
		if !ok {
			return
		}
		msg = &waE2E.Message{ImageMessage: &waE2E.ImageMessage{
			Caption: proto.String(firstNonEmptyOf(b.Caption, b.Text)), Mimetype: proto.String("image/jpeg"),
			URL: &up.URL, DirectPath: &up.DirectPath, MediaKey: up.MediaKey,
			FileEncSHA256: up.FileEncSHA256, FileSHA256: up.FileSHA256, FileLength: proto.Uint64(up.FileLength),
		}}
	} else {
		msg = &waE2E.Message{Conversation: proto.String(b.Text)}
	}
	raw, err := protojson.Marshal(msg)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	id, err := sess.mgr.store.enqueueScheduled(r.Context(), sess.id, jid.String(), raw, sendAt, nowMillis())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "sendAt": sendAt, "status": "pending"})
}

// GET /api/sessions/{sid}/schedule
func (s *server) handleListScheduled(w http.ResponseWriter, r *http.Request) {
	sess := s.sessionByID(w, r.PathValue("sid"))
	if sess == nil {
		return
	}
	rows, err := sess.mgr.store.listScheduled(r.Context(), sess.id, 200)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"scheduled": rows})
}

// DELETE /api/sessions/{sid}/schedule/{id}
func (s *server) handleCancelScheduled(w http.ResponseWriter, r *http.Request) {
	sess := s.sessionByID(w, r.PathValue("sid"))
	if sess == nil {
		return
	}
	id := parseInt64(r.PathValue("id"))
	ok, err := sess.mgr.store.cancelScheduled(r.Context(), sess.id, id)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "agendamento não encontrado ou já processado"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "canceled"})
}

func parseInt64(s string) int64 {
	n, _ := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	return n
}

// normalizeEpochMillis aceita epoch em segundos ou milissegundos e devolve ms.
func normalizeEpochMillis(v int64) int64 {
	if v > 0 && v < 1_000_000_000_000 { // < ~2001 em ms => veio em segundos
		return v * 1000
	}
	return v
}

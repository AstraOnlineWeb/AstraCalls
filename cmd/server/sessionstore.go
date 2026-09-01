package main

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
)

type sessionRow struct {
	ID        string
	Name      string
	JID       string
	LastJID   string
	Webhook   string
	Chatwoot  string
	Recording bool
	Proxy     string
	SIPUser   string
	SIPPass   string
}

type sessionStore struct{ db *sql.DB }

// newSessionStore cria a tabela de config das sessões no banco PRINCIPAL.
// (O store do whatsmeow de cada sessão fica em um banco separado — ver db.go.)
func newSessionStore(ctx context.Context, db *sql.DB) (*sessionStore, error) {
	_, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS sessions (
		id         TEXT PRIMARY KEY,
		name       TEXT NOT NULL,
		jid        TEXT,
		webhook    TEXT,
		chatwoot   TEXT,
		sip_user   TEXT,
		sip_pass   TEXT,
		created_at TIMESTAMPTZ NOT NULL DEFAULT now()
	)`)
	if err != nil {
		return nil, err
	}
	// migração p/ bancos antigos (Postgres aceita IF NOT EXISTS no ADD COLUMN)
	_, _ = db.ExecContext(ctx, `ALTER TABLE sessions ADD COLUMN IF NOT EXISTS webhook TEXT`)
	_, _ = db.ExecContext(ctx, `ALTER TABLE sessions ADD COLUMN IF NOT EXISTS chatwoot TEXT`)
	_, _ = db.ExecContext(ctx, `ALTER TABLE sessions ADD COLUMN IF NOT EXISTS sip_user TEXT`)
	_, _ = db.ExecContext(ctx, `ALTER TABLE sessions ADD COLUMN IF NOT EXISTS sip_pass TEXT`)
	_, _ = db.ExecContext(ctx, `ALTER TABLE sessions ADD COLUMN IF NOT EXISTS created_at TIMESTAMPTZ NOT NULL DEFAULT now()`)
	_, _ = db.ExecContext(ctx, `ALTER TABLE sessions ADD COLUMN IF NOT EXISTS recording BOOLEAN NOT NULL DEFAULT false`)
	_, _ = db.ExecContext(ctx, `ALTER TABLE sessions ADD COLUMN IF NOT EXISTS proxy TEXT`)
	// último número conectado — sobrevive à desconexão p/ o painel exibir "último número"
	_, _ = db.ExecContext(ctx, `ALTER TABLE sessions ADD COLUMN IF NOT EXISTS last_jid TEXT`)

	// Histórico de mensagens (para as rotas de chats/messages).
	// O whatsmeow não persiste histórico; guardamos aqui o que passa pela sessão.
	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS messages (
		session_id TEXT NOT NULL,
		chat_jid   TEXT NOT NULL,
		sender_jid TEXT NOT NULL,
		msg_id     TEXT NOT NULL,
		from_me    BOOLEAN NOT NULL,
		ts         BIGINT NOT NULL,
		type       TEXT NOT NULL,
		body       TEXT,
		raw        JSONB,
		PRIMARY KEY (session_id, chat_jid, msg_id)
	)`); err != nil {
		return nil, err
	}
	_, _ = db.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS messages_chat_ts ON messages (session_id, chat_jid, ts DESC)`)
	_, _ = db.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS messages_session_ts ON messages (session_id, ts DESC)`)

	// Fila de reentrega das entradas do Chatwoot (ver chatwoot_outbox.go). Sobrevive
	// a restart do AstraCalls: se o Chatwoot cai, a mensagem fica aqui e é reentregue
	// com backoff em vez de se perder.
	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS chatwoot_outbox (
		id         BIGSERIAL PRIMARY KEY,
		session_id TEXT NOT NULL,
		source_id  TEXT NOT NULL,
		payload    JSONB NOT NULL,
		attempts   INT NOT NULL DEFAULT 0,
		next_at    BIGINT NOT NULL,
		created_at BIGINT NOT NULL,
		last_error TEXT,
		dead       BOOLEAN NOT NULL DEFAULT false,
		UNIQUE (session_id, source_id)
	)`); err != nil {
		return nil, err
	}
	_, _ = db.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS chatwoot_outbox_due ON chatwoot_outbox (dead, next_at)`)

	return &sessionStore{db: db}, nil
}

// genSIPCredential gera um token hex aleatório para usuário/senha SIP.
func genSIPCredential(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// generateRandomString devolve uma string hex aleatória com n caracteres.
func generateRandomString(n int) string {
	b := make([]byte, (n+1)/2)
	rand.Read(b)
	return hex.EncodeToString(b)[:n]
}

func newSessionID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func (s *sessionStore) list(ctx context.Context) ([]sessionRow, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, COALESCE(jid, ''), COALESCE(last_jid, ''), COALESCE(webhook, ''), COALESCE(chatwoot, ''), COALESCE(recording, false), COALESCE(proxy, ''), COALESCE(sip_user, ''), COALESCE(sip_pass, '') FROM sessions ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []sessionRow
	for rows.Next() {
		var r sessionRow
		if err := rows.Scan(&r.ID, &r.Name, &r.JID, &r.LastJID, &r.Webhook, &r.Chatwoot, &r.Recording, &r.Proxy, &r.SIPUser, &r.SIPPass); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// insert cria a sessão já com credenciais SIP geradas e as devolve.
func (s *sessionStore) insert(ctx context.Context, id, name string) (sipUser, sipPass string, err error) {
	sipUser = "wa_" + genSIPCredential(4)
	sipPass = genSIPCredential(12)
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO sessions (id, name, jid, sip_user, sip_pass) VALUES ($1, $2, NULL, $3, $4)`,
		id, name, sipUser, sipPass)
	if err != nil {
		return "", "", err
	}
	return sipUser, sipPass, nil
}

func (s *sessionStore) setSIP(ctx context.Context, id, sipUser, sipPass string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE sessions SET sip_user = $1, sip_pass = $2 WHERE id = $3`, sipUser, sipPass, id)
	return err
}

func (s *sessionStore) setJID(ctx context.Context, id, jid string) error {
	// ao conectar (jid != ''), atualiza também o last_jid; ao desconectar (jid=''),
	// preserva o last_jid para o painel mostrar o último número.
	_, err := s.db.ExecContext(ctx,
		`UPDATE sessions SET jid = $1, last_jid = CASE WHEN $1 <> '' THEN $1 ELSE last_jid END WHERE id = $2`, jid, id)
	return err
}

func (s *sessionStore) setWebhook(ctx context.Context, id, url string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE sessions SET webhook = $1 WHERE id = $2`, url, id)
	return err
}

func (s *sessionStore) setChatwoot(ctx context.Context, id, cfgJSON string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE sessions SET chatwoot = $1 WHERE id = $2`, cfgJSON, id)
	return err
}

func (s *sessionStore) setRecording(ctx context.Context, id string, on bool) error {
	_, err := s.db.ExecContext(ctx, `UPDATE sessions SET recording = $1 WHERE id = $2`, on, id)
	return err
}

func (s *sessionStore) setProxy(ctx context.Context, id, proxyURL string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE sessions SET proxy = $1 WHERE id = $2`, proxyURL, id)
	return err
}

func (s *sessionStore) delete(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE id = $1`, id)
	return err
}

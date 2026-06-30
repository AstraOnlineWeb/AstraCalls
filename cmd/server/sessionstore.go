package main

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
)

type sessionRow struct {
	ID       string
	Name     string
	JID      string
	Webhook  string
	Chatwoot string
	SIPUser  string
	SIPPass  string
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
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, COALESCE(jid, ''), COALESCE(webhook, ''), COALESCE(chatwoot, ''), COALESCE(sip_user, ''), COALESCE(sip_pass, '') FROM sessions ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []sessionRow
	for rows.Next() {
		var r sessionRow
		if err := rows.Scan(&r.ID, &r.Name, &r.JID, &r.Webhook, &r.Chatwoot, &r.SIPUser, &r.SIPPass); err != nil {
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
	_, err := s.db.ExecContext(ctx, `UPDATE sessions SET jid = $1 WHERE id = $2`, jid, id)
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

func (s *sessionStore) delete(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE id = $1`, id)
	return err
}

package main

import (
	"context"
	"database/sql"
	"encoding/json"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/encoding/protojson"
)

// storeMessageEvent persiste uma mensagem recebida (evento do whatsmeow) no
// histórico. Roda em background para não travar o loop de eventos.
func (s *Session) storeMessageEvent(evt *events.Message) {
	if s.mgr.store == nil {
		return
	}
	m := storedMessage{
		ChatJID:   evt.Info.Chat.String(),
		SenderJID: evt.Info.Sender.String(),
		MsgID:     evt.Info.ID,
		FromMe:    evt.Info.IsFromMe,
		Timestamp: evt.Info.Timestamp.UnixMilli(),
		Type:      messageType(evt.Message),
		Body:      messageText(evt.Message),
	}
	if raw, err := protojson.Marshal(evt.Message); err == nil {
		m.Raw = json.RawMessage(raw)
	}
	go func() { _ = s.mgr.store.saveMessage(s.mgr.appCtx, s.id, m) }()
}

// recordOutgoing persiste uma mensagem que ESTE cliente enviou. Só grava
// conteúdo de fato (texto/mídia/etc.); ações como reação, edição e revogação
// caem em type "unknown" e são ignoradas para não poluir o histórico.
func (s *Session) recordOutgoing(chat types.JID, msgID string, ts int64, msg *waE2E.Message) {
	if s.mgr.store == nil {
		return
	}
	typ := messageType(msg)
	if typ == "unknown" {
		return
	}
	sender := chat
	if id := s.client.Store.ID; id != nil {
		sender = id.ToNonAD()
	}
	m := storedMessage{
		ChatJID:   chat.String(),
		SenderJID: sender.String(),
		MsgID:     msgID,
		FromMe:    true,
		Timestamp: ts,
		Type:      typ,
		Body:      messageText(msg),
	}
	if raw, err := protojson.Marshal(msg); err == nil {
		m.Raw = json.RawMessage(raw)
	}
	go func() { _ = s.mgr.store.saveMessage(s.mgr.appCtx, s.id, m) }()
}

// storedMessage é uma linha da tabela messages.
type storedMessage struct {
	ChatJID   string          `json:"chat"`
	SenderJID string          `json:"sender"`
	MsgID     string          `json:"id"`
	FromMe    bool            `json:"fromMe"`
	Timestamp int64           `json:"timestamp"`
	Type      string          `json:"type"`
	Body      string          `json:"body"`
	Raw       json.RawMessage `json:"raw,omitempty"`
}

// chatOverview resume uma conversa (última mensagem + contagem).
type chatOverview struct {
	ChatJID    string `json:"chat"`
	LastBody   string `json:"lastMessage"`
	LastType   string `json:"lastType"`
	LastTS     int64  `json:"timestamp"`
	Count      int    `json:"count"`
	LastFromMe bool   `json:"lastFromMe"`
}

// saveMessage persiste (ou atualiza) uma mensagem. Idempotente por (session, chat, msg_id).
func (s *sessionStore) saveMessage(ctx context.Context, sessionID string, m storedMessage) error {
	var raw any
	if len(m.Raw) > 0 {
		raw = []byte(m.Raw)
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO messages (session_id, chat_jid, sender_jid, msg_id, from_me, ts, type, body, raw)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		ON CONFLICT (session_id, chat_jid, msg_id) DO UPDATE
			SET body = EXCLUDED.body, type = EXCLUDED.type, raw = EXCLUDED.raw`,
		sessionID, m.ChatJID, m.SenderJID, m.MsgID, m.FromMe, m.Timestamp, m.Type, m.Body, raw)
	return err
}

// listMessages devolve as mensagens de um chat, mais recentes primeiro.
func (s *sessionStore) listMessages(ctx context.Context, sessionID, chatJID string, limit, offset int, withRaw bool) ([]storedMessage, error) {
	rawCol := "NULL"
	if withRaw {
		rawCol = "raw"
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT chat_jid, sender_jid, msg_id, from_me, ts, type, COALESCE(body, ''), `+rawCol+`
		FROM messages WHERE session_id = $1 AND chat_jid = $2
		ORDER BY ts DESC LIMIT $3 OFFSET $4`, sessionID, chatJID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanMessages(rows)
}

func scanMessages(rows *sql.Rows) ([]storedMessage, error) {
	out := []storedMessage{}
	for rows.Next() {
		var m storedMessage
		var raw []byte
		if err := rows.Scan(&m.ChatJID, &m.SenderJID, &m.MsgID, &m.FromMe, &m.Timestamp, &m.Type, &m.Body, &raw); err != nil {
			return nil, err
		}
		if len(raw) > 0 {
			m.Raw = json.RawMessage(raw)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// listChats devolve uma visão geral das conversas (uma linha por chat_jid),
// ordenadas pela última mensagem.
func (s *sessionStore) listChats(ctx context.Context, sessionID string, limit, offset int) ([]chatOverview, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT m.chat_jid, m.body, m.type, m.ts, m.from_me, c.cnt
		FROM messages m
		JOIN (
			SELECT chat_jid, MAX(ts) AS max_ts, COUNT(*) AS cnt
			FROM messages WHERE session_id = $1 GROUP BY chat_jid
		) c ON c.chat_jid = m.chat_jid AND c.max_ts = m.ts
		WHERE m.session_id = $1
		ORDER BY m.ts DESC LIMIT $2 OFFSET $3`, sessionID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []chatOverview{}
	for rows.Next() {
		var o chatOverview
		var body sql.NullString
		if err := rows.Scan(&o.ChatJID, &body, &o.LastType, &o.LastTS, &o.LastFromMe, &o.Count); err != nil {
			return nil, err
		}
		o.LastBody = body.String
		out = append(out, o)
	}
	return out, rows.Err()
}

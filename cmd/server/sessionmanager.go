package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"
	waLog "go.mau.fi/whatsmeow/util/log"
)

type SessionManager struct {
	appCtx   context.Context
	db       *dbProvider
	broker   *Broker
	store    *sessionStore
	waLogger waLog.Logger
	log      *slog.Logger
	maxCalls int

	// sipInbound, se definido, é chamado quando o WhatsApp recebe uma chamada,
	// para tocar num softphone/PBX SIP registrado (gateway SIP).
	sipInbound func(sess *Session, callID, peerNumber string)

	mu       sync.RWMutex
	sessions map[string]*Session
	order    []string
}

func newSessionManager(ctx context.Context, db *dbProvider, broker *Broker, store *sessionStore, waLogger waLog.Logger, log *slog.Logger, maxCalls int) *SessionManager {
	return &SessionManager{
		appCtx:   ctx,
		db:       db,
		broker:   broker,
		store:    store,
		waLogger: waLogger,
		log:      log,
		maxCalls: maxCalls,
		sessions: map[string]*Session{},
	}
}

func (m *SessionManager) register(s *Session) {
	m.mu.Lock()
	m.sessions[s.id] = s
	m.order = append(m.order, s.id)
	m.mu.Unlock()
}

func (m *SessionManager) unregister(id string) {
	m.mu.Lock()
	delete(m.sessions, id)
	for i, x := range m.order {
		if x == id {
			m.order = append(m.order[:i], m.order[i+1:]...)
			break
		}
	}
	m.mu.Unlock()
}

func (m *SessionManager) sessionForChatwootAccount(accountID int) *Session {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, s := range m.sessions {
		if c := s.getChatwoot(); c.valid() && c.AccountID == accountID {
			return s
		}
	}
	return nil
}

// sessionForChatwootInbox: sessão amarrada à conta E à caixa (inbox) específica.
// Usada para que o widget só apareça/ligue na caixa que tem WhatsApp conectado.
func (m *SessionManager) sessionForChatwootInbox(accountID, inboxID int) *Session {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, s := range m.sessions {
		if c := s.getChatwoot(); c.valid() && c.AccountID == accountID && c.InboxID == inboxID {
			return s
		}
	}
	return nil
}

func (m *SessionManager) Get(id string) (*Session, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	s, ok := m.sessions[id]
	return s, ok
}

func (m *SessionManager) infos() []SessionInfo {
	m.mu.RLock()
	ordered := make([]*Session, 0, len(m.order))
	for _, id := range m.order {
		if s, ok := m.sessions[id]; ok {
			ordered = append(ordered, s)
		}
	}
	m.mu.RUnlock()
	out := make([]SessionInfo, 0, len(ordered))
	for _, s := range ordered {
		out = append(out, s.info())
	}
	return out
}

func (m *SessionManager) snapshotEvents() []any {
	return []any{map[string]any{"type": "session-list", "sessions": m.infos()}}
}

func (m *SessionManager) Restore(ctx context.Context) error {
	rows, err := m.store.list(ctx)
	if err != nil {
		return err
	}
	for _, row := range rows {
		if row.JID == "" {
			_ = m.db.dropSessionDB(ctx, row.ID)
			_ = m.store.delete(ctx, row.ID)
			continue
		}
		if _, err := types.ParseJID(row.JID); err != nil {
			m.log.Warn("dropping session with unparseable jid", "session", row.ID, "jid", row.JID)
			_ = m.db.dropSessionDB(ctx, row.ID)
			_ = m.store.delete(ctx, row.ID)
			continue
		}
		container, db, err := m.db.openSessionContainer(ctx, row.ID)
		if err != nil {
			m.log.Error("opening session database failed", "session", row.ID, "err", err)
			continue
		}
		device, err := container.GetFirstDevice(ctx)
		if err != nil || device == nil || device.ID == nil {
			m.log.Warn("dropping session with no stored device", "session", row.ID, "jid", row.JID, "err", err)
			_ = db.Close()
			_ = m.db.dropSessionDB(ctx, row.ID)
			_ = m.store.delete(ctx, row.ID)
			continue
		}
		client := whatsmeow.NewClient(device, m.waLogger)
		s := newSession(m, row.ID, row.Name, client)
		s.waContainer = container
		s.waDB = db
		// backfill de credenciais SIP para sessões criadas antes do recurso.
		if row.SIPUser == "" {
			row.SIPUser = "wa_" + genSIPCredential(4)
			row.SIPPass = genSIPCredential(12)
			_ = m.store.setSIP(ctx, row.ID, row.SIPUser, row.SIPPass)
		}
		s.SIPUser = row.SIPUser
		s.SIPPass = row.SIPPass
		s.setWebhook(row.Webhook)
		if row.Chatwoot != "" {
			var cfg ChatwootConfig
			if json.Unmarshal([]byte(row.Chatwoot), &cfg) == nil {
				s.setChatwoot(cfg)
			}
		}
		m.register(s)
		if err := s.connect(ctx); err != nil {
			m.log.Error("session connect failed", "session", row.ID, "err", err)
		}
	}
	m.broker.emitSessionList(m.infos())
	m.log.Info("sessions restored", "count", len(m.infos()))
	return nil
}

func (m *SessionManager) Create(name string) (string, error) {
	id := newSessionID()
	sipUser, sipPass, err := m.store.insert(m.appCtx, id, name)
	if err != nil {
		return "", err
	}
	container, db, err := m.db.openSessionContainer(m.appCtx, id)
	if err != nil {
		_ = m.store.delete(m.appCtx, id)
		_ = m.db.dropSessionDB(m.appCtx, id)
		return "", fmt.Errorf("create session store: %w", err)
	}
	device := container.NewDevice()
	client := whatsmeow.NewClient(device, m.waLogger)
	s := newSession(m, id, name, client)
	s.waContainer = container
	s.waDB = db
	s.SIPUser = sipUser
	s.SIPPass = sipPass
	m.register(s)
	m.broker.emitSessionList(m.infos())
	if err := s.startPairing(m.appCtx); err != nil {
		m.log.Error("start pairing failed", "session", id, "err", err)
		return "", fmt.Errorf("start pairing: %w", err)
	}
	m.log.Info("session created", "session", id, "name", name)
	return id, nil
}

func (m *SessionManager) Delete(ctx context.Context, id string) error {
	s, ok := m.Get(id)
	if !ok {
		return fmt.Errorf("no session %s", id)
	}
	if s.client.Store.ID != nil {
		if err := s.client.Logout(ctx); err != nil {
			m.log.Warn("logout failed; deleting locally", "session", id, "err", err)
		}
	}
	s.client.Disconnect()
	s.teardownAllCalls()
	// o store da sessão é um banco inteiro só dela: fecha a conexão e derruba.
	if s.waDB != nil {
		_ = s.waDB.Close()
	}
	if err := m.db.dropSessionDB(ctx, id); err != nil {
		m.log.Warn("drop session database failed", "session", id, "err", err)
	}
	m.unregister(id)
	_ = m.store.delete(ctx, id)
	m.broker.emitSessionList(m.infos())
	m.log.Info("session deleted", "session", id)
	return nil
}

func (m *SessionManager) Logout(ctx context.Context, id string) error {
	s, ok := m.Get(id)
	if !ok {
		return fmt.Errorf("no session %s", id)
	}
	if s.client.Store.ID != nil {
		if err := s.client.Logout(ctx); err != nil {
			m.log.Warn("logout failed", "session", id, "err", err)
		}
	}
	s.replaceClient(whatsmeow.NewClient(s.waContainer.NewDevice(), m.waLogger))
	_ = m.store.setJID(ctx, id, "")
	s.setAuth(AuthSnapshot{State: "logged_out", Paired: false})
	m.log.Info("session disconnected", "session", id)
	return nil
}

func (m *SessionManager) Pair(id string) error {
	s, ok := m.Get(id)
	if !ok {
		return fmt.Errorf("no session %s", id)
	}
	if s.client.Store.ID != nil {
		return fmt.Errorf("session already paired")
	}
	s.replaceClient(whatsmeow.NewClient(s.waContainer.NewDevice(), m.waLogger))
	if err := s.startPairing(m.appCtx); err != nil {
		return fmt.Errorf("start pairing: %w", err)
	}
	m.broker.emitSessionList(m.infos())
	m.log.Info("session re-pairing", "session", id)
	return nil
}

func (m *SessionManager) disconnectAll() {
	m.mu.RLock()
	all := make([]*Session, 0, len(m.sessions))
	for _, s := range m.sessions {
		all = append(all, s)
	}
	m.mu.RUnlock()
	for _, s := range all {
		s.shutdown()
	}
}

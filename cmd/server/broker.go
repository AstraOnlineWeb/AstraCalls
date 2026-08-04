package main

import (
	"encoding/json"
	"net/http"
	"sync"
	"time"
)

type CallStatus string

const (
	StatusStarting  CallStatus = "starting"
	StatusRinging   CallStatus = "ringing"
	StatusConnected CallStatus = "connected"
	StatusEnded     CallStatus = "ended"
)

type CallRecord struct {
	SessionID string     `json:"sessionId"`
	CallID    string     `json:"callId"`
	Owner     *string    `json:"owner"`
	Direction string     `json:"direction"`
	Peer      string     `json:"peer"`
	StartedAt int64      `json:"startedAt"`
	Status    CallStatus `json:"status"`
	Held      bool       `json:"held"`
	EndedAt   *int64     `json:"endedAt,omitempty"`
	EndReason string     `json:"endReason,omitempty"`
}

type AuthSnapshot struct {
	State   string          `json:"state"`
	Paired  bool            `json:"paired"`
	QR      string          `json:"qr,omitempty"`
	Code    string          `json:"code,omitempty"`    // código de pareamento por telefone (8 dígitos)
	Passkey json.RawMessage `json:"passkey,omitempty"` // desafio WebAuthn (publicKey) p/ contas com passkey
}

type SessionInfo struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	JID       string `json:"jid"`
	State     string `json:"state"`
	Paired    bool   `json:"paired"`
	Recording bool   `json:"recording"`
}

type subscriber struct {
	clientID string
	// accountID é a conta do Chatwoot que este assinante representa. 0 = sem
	// escopo (painel admin) e recebe TODOS os eventos; > 0 (widget de uma conta)
	// recebe só os eventos de chamada das sessões daquela conta.
	accountID int
	ch        chan []byte
}

type Broker struct {
	mu      sync.RWMutex
	subs    map[*subscriber]struct{}
	calls   map[string]*CallRecord
	history []CallRecord

	SnapshotFn func() []any
	// AccountForSession resolve o account_id do Chatwoot de uma sessão (0 = nenhum).
	// Injetado pelo server para escopar os eventos de chamada por conta.
	AccountForSession func(sessionID string) int
}

func NewBroker() *Broker {
	return &Broker{
		subs:  map[*subscriber]struct{}{},
		calls: map[string]*CallRecord{},
	}
}

func (b *Broker) subscribe(clientID string, accountID int) *subscriber {
	s := &subscriber{clientID: clientID, accountID: accountID, ch: make(chan []byte, 32)}
	b.mu.Lock()
	b.subs[s] = struct{}{}
	b.mu.Unlock()
	return s
}

func (b *Broker) unsubscribe(s *subscriber) {
	b.mu.Lock()
	delete(b.subs, s)
	b.mu.Unlock()
	close(s.ch)
}

func (b *Broker) broadcast(ev any) {
	data, err := json.Marshal(ev)
	if err != nil {
		return
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	for s := range b.subs {
		select {
		case s.ch <- data:
		default:
		}
	}
}

// broadcastForSession entrega um evento apenas aos assinantes com escopo compatível
// com a conta da sessão que o originou: os sem escopo (accountID 0 = painel admin)
// recebem sempre; um widget de conta só recebe se for a MESMA conta da sessão.
// Corrige o vazamento em que a chamada recebida de uma empresa tocava no widget de
// outra empresa que compartilha a mesma API key.
func (b *Broker) broadcastForSession(sessionID string, ev any) {
	data, err := json.Marshal(ev)
	if err != nil {
		return
	}
	acct := 0
	if b.AccountForSession != nil {
		acct = b.AccountForSession(sessionID)
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	for s := range b.subs {
		if s.accountID != 0 && s.accountID != acct {
			continue // widget de outra conta: não deve tocar/abrir
		}
		select {
		case s.ch <- data:
		default:
		}
	}
}

// broadcastForSessionTargeted é como broadcastForSession, mas se targetClientID != ""
// entrega o evento SÓ ao assinante daquele navegador (dentro do escopo de conta). Usado
// para direcionar a oferta de transferência a um atendente específico.
func (b *Broker) broadcastForSessionTargeted(sessionID, targetClientID string, ev any) {
	if targetClientID == "" {
		b.broadcastForSession(sessionID, ev)
		return
	}
	data, err := json.Marshal(ev)
	if err != nil {
		return
	}
	acct := 0
	if b.AccountForSession != nil {
		acct = b.AccountForSession(sessionID)
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	for s := range b.subs {
		if s.accountID != 0 && s.accountID != acct {
			continue
		}
		if s.clientID != targetClientID {
			continue
		}
		select {
		case s.ch <- data:
		default:
		}
	}
}

// emitTransferOffer avisa o(s) painel(is) que uma chamada ativa foi transferida e está
// disponível para atender (pickup). Se target != "", só o navegador alvo recebe.
func (b *Broker) emitTransferOffer(sessionID, id, peer, from, target string) {
	b.broadcastForSessionTargeted(sessionID, target, map[string]any{
		"type": "call-transfer-offer", "sessionId": sessionID, "id": id, "peer": peer,
		"from": from, "offeredAt": time.Now().UnixMilli(),
	})
}

// emitTransferClaimed some com a oferta de transferência nos outros painéis quando
// alguém atende.
func (b *Broker) emitTransferClaimed(sessionID, id, owner string) {
	b.broadcastForSession(sessionID, map[string]any{"type": "call-transfer-claimed", "sessionId": sessionID, "id": id, "owner": owner})
}

func (b *Broker) emitAuthState(sessionID string, a AuthSnapshot) {
	ev := map[string]any{
		"type": "auth-state", "sessionId": sessionID,
		"paired": a.Paired, "state": a.State, "qr": a.QR,
	}
	if len(a.Passkey) > 0 {
		ev["passkey"] = a.Passkey // desafio WebAuthn p/ contas com passkey
	}
	b.broadcast(ev)
}

func (b *Broker) emitSessionList(sessions []SessionInfo) {
	b.broadcast(map[string]any{"type": "session-list", "sessions": sessions})
}

func (b *Broker) emitSessionQR(sessionID, qr string) {
	b.broadcast(map[string]any{"type": "session-qr", "sessionId": sessionID, "qr": qr})
}

func (b *Broker) upsertCall(r CallRecord) {
	b.mu.Lock()
	cp := r
	b.calls[r.CallID] = &cp
	b.mu.Unlock()
	b.broadcastCallList()
	b.broadcast(map[string]any{
		"type": "call-status", "sessionId": r.SessionID, "id": r.CallID, "owner": r.Owner,
		"status": r.Status, "peer": r.Peer, "startedAt": r.StartedAt, "held": r.Held,
	})
}

func (b *Broker) getCall(id string) (*CallRecord, bool) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	c, ok := b.calls[id]
	if !ok {
		return nil, false
	}
	cp := *c
	return &cp, true
}

func (b *Broker) setOwner(id, owner string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	c, ok := b.calls[id]
	if !ok {
		return false
	}
	if c.Owner != nil && *c.Owner != owner {
		return false
	}
	c.Owner = &owner
	return true
}

// reassignOwner troca (ou limpa, com owner=nil) o dono de uma chamada sem as travas
// do setOwner, e re-emite call-list/call-status para os painéis refletirem. Usado na
// transferência: liberar o dono ao ofertar e fixar o novo dono no pickup.
func (b *Broker) reassignOwner(id string, owner *string) bool {
	b.mu.Lock()
	c, ok := b.calls[id]
	if !ok {
		b.mu.Unlock()
		return false
	}
	c.Owner = owner
	rec := *c
	b.mu.Unlock()
	b.broadcastCallList()
	b.broadcast(map[string]any{
		"type": "call-status", "sessionId": rec.SessionID, "id": rec.CallID, "owner": rec.Owner,
		"status": rec.Status, "peer": rec.Peer, "startedAt": rec.StartedAt, "held": rec.Held,
	})
	return true
}

func (b *Broker) ownerActiveCall(owner string) string {
	if owner == "" {
		return ""
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	for id, c := range b.calls {
		if c.Owner != nil && *c.Owner == owner && c.Status != StatusEnded {
			return id
		}
	}
	return ""
}

func (b *Broker) endCall(id, reason string) {
	b.mu.Lock()
	c, ok := b.calls[id]
	if !ok {
		b.mu.Unlock()
		return
	}
	now := time.Now().UnixMilli()
	c.Status = StatusEnded
	c.EndedAt = &now
	c.EndReason = reason
	ended := *c
	delete(b.calls, id)
	b.history = append(b.history, ended)
	owner := c.Owner
	sessionID := c.SessionID
	b.mu.Unlock()

	b.broadcast(map[string]any{
		"type": "call-ended", "sessionId": sessionID, "id": id, "owner": owner, "reason": reason, "endedAt": now,
	})
	b.broadcastCallList()
}

func (b *Broker) broadcastCallList() {
	b.mu.RLock()
	list := make([]CallRecord, 0, len(b.calls))
	for _, c := range b.calls {
		list = append(list, *c)
	}
	b.mu.RUnlock()
	b.broadcast(map[string]any{"type": "call-list", "calls": list})
}

func (b *Broker) emitIncoming(sessionID, id, peer, phone, name string, video bool) {
	// escopado por conta: só toca no widget da empresa dona da sessão.
	// phone/name resolvidos (issue #9): o widget mostra o número/nome em vez do
	// LID cru; peer segue no payload por compatibilidade.
	b.broadcastForSession(sessionID, map[string]any{
		"type": "incoming", "sessionId": sessionID, "id": id, "peer": peer,
		"phone": phone, "name": name,
		"video": video, "offeredAt": time.Now().UnixMilli(),
	})
}

// emitVideoState avisa a UI sobre negociação de vídeo mid-call (pedido de upgrade,
// câmera do peer ligada/desligada, etc.). Escopado por conta como as chamadas.
func (b *Broker) emitVideoState(sessionID, id, kind string, peerVideo, localVideo, upgradeIncoming, upgradeOutgoing bool) {
	b.broadcastForSession(sessionID, map[string]any{
		"type": "video-state", "sessionId": sessionID, "id": id, "kind": kind,
		"peerVideo": peerVideo, "localVideo": localVideo,
		"upgradeIncoming": upgradeIncoming, "upgradeOutgoing": upgradeOutgoing,
	})
}

func (b *Broker) emitIncomingClaimed(sessionID, id, owner string) {
	b.broadcastForSession(sessionID, map[string]any{"type": "incoming-claimed", "sessionId": sessionID, "id": id, "owner": owner})
}

func (b *Broker) historyRows(sessionID string, limit int) []CallRecord {
	b.mu.RLock()
	defer b.mu.RUnlock()
	rows := make([]CallRecord, 0, limit)
	for i := len(b.history) - 1; i >= 0 && len(rows) < limit; i-- {
		if sessionID == "" || b.history[i].SessionID == sessionID {
			rows = append(rows, b.history[i])
		}
	}
	return rows
}

func (b *Broker) serveSSE(w http.ResponseWriter, r *http.Request, clientID string, accountID int) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	sub := b.subscribe(clientID, accountID)
	defer b.unsubscribe(sub)

	if b.SnapshotFn != nil {
		for _, ev := range b.SnapshotFn() {
			writeSSE(w, flusher, ev)
		}
	}
	b.broadcastCallList()

	keepalive := time.NewTicker(20 * time.Second)
	defer keepalive.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case data := <-sub.ch:
			if _, err := w.Write(append(append([]byte("data: "), data...), '\n', '\n')); err != nil {
				return
			}
			flusher.Flush()
		case <-keepalive.C:
			w.Write([]byte(": ping\n\n"))
			flusher.Flush()
		}
	}
}

func writeSSE(w http.ResponseWriter, f http.Flusher, ev any) {
	data, _ := json.Marshal(ev)
	w.Write(append(append([]byte("data: "), data...), '\n', '\n'))
	f.Flush()
}

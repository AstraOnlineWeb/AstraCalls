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
	Source    string     `json:"source,omitempty"` // "sip" no modo agente; vazio = WhatsApp
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
	LastJID   string `json:"lastJid"` // último número conectado (persiste após desconectar)
	State     string `json:"state"`
	Paired    bool   `json:"paired"`
	Recording bool   `json:"recording"`
	SIPUser   string `json:"sip_user"`
	SIPPass   string `json:"sip_pass"`
	SIPURL    string `json:"sip_url"`
	// Modelo 2 (registro em PBX externo).
	SIPExtEnabled bool   `json:"sip_ext_enabled"`
	SIPExtHost    string `json:"sip_ext_host"`
	SIPExtPort    int    `json:"sip_ext_port"`
	SIPExtUser    string `json:"sip_ext_user"`
	SIPExtPass    string `json:"sip_ext_pass"`
	SIPExtDest    string `json:"sip_ext_dest"`
	SIPExtProxy   string `json:"sip_ext_proxy"`
	SIPExtStatus  string `json:"sip_ext_status"`
	SIPExtError   string `json:"sip_ext_error"`
	// modo do INVITE entrante: originate | agent | auto.
	SIPExtInboundMode string `json:"sip_ext_inbound_mode"`
}

type subscriber struct {
	clientID string
	// accountID é a conta do Chatwoot que este assinante representa. 0 = sem
	// escopo (painel admin) e recebe TODOS os eventos; > 0 (widget de uma conta)
	// recebe só os eventos de chamada das sessões daquela conta.
	accountID int
	// cwHost é o host do Chatwoot do widget ("" = não identificado): com ele, conta 1
	// de um Chatwoot não recebe as chamadas da conta 1 de outro Chatwoot.
	cwHost string
	// inboxes restringe o widget às caixas (inbox) do Chatwoot que o atendente
	// atende (nil = todas as caixas da conta): agente da caixa A não toca com
	// chamada da caixa B da mesma empresa.
	inboxes map[int]bool
	ch      chan []byte
}

// wants diz se o assinante deve receber um evento da sessão com esse escopo.
func (s *subscriber) wants(sc sessionScope) bool {
	if s.accountID == 0 {
		return true // painel admin
	}
	if s.accountID != sc.acct {
		return false
	}
	if s.cwHost != "" && sc.host != "" && s.cwHost != sc.host {
		return false
	}
	return s.inboxes == nil || s.inboxes[sc.inbox]
}

// sessionScope é a conta/host/caixa do Chatwoot ligada a uma sessão.
type sessionScope struct {
	acct  int
	host  string
	inbox int
}

type Broker struct {
	mu      sync.RWMutex
	subs    map[*subscriber]struct{}
	calls   map[string]*CallRecord
	history []CallRecord

	// SessionsFn lista as sessões para o retrato inicial do SSE (filtrado por escopo).
	SessionsFn func() []SessionInfo
	// AccountForSession resolve o account_id do Chatwoot de uma sessão (0 = nenhum).
	// Injetado pelo server para escopar os eventos de chamada por conta.
	AccountForSession func(sessionID string) int
	// HostForSession resolve o host do Chatwoot da sessão ("" = nenhum).
	HostForSession func(sessionID string) string
	// InboxForSession resolve a caixa (inbox) do Chatwoot da sessão (0 = nenhuma).
	InboxForSession func(sessionID string) int
}

func (b *Broker) scopeOf(sessionID string) (sc sessionScope) {
	if b.AccountForSession != nil {
		sc.acct = b.AccountForSession(sessionID)
	}
	if b.HostForSession != nil {
		sc.host = b.HostForSession(sessionID)
	}
	if b.InboxForSession != nil {
		sc.inbox = b.InboxForSession(sessionID)
	}
	return
}

func NewBroker() *Broker {
	return &Broker{
		subs:  map[*subscriber]struct{}{},
		calls: map[string]*CallRecord{},
	}
}

func (b *Broker) subscribe(clientID string, accountID int) *subscriber {
	return b.subscribeScoped(clientID, accountID, "")
}

func (b *Broker) subscribeScoped(clientID string, accountID int, cwHost string) *subscriber {
	return b.subscribeInboxes(clientID, accountID, cwHost, nil)
}

func (b *Broker) subscribeInboxes(clientID string, accountID int, cwHost string, inboxes map[int]bool) *subscriber {
	s := &subscriber{clientID: clientID, accountID: accountID, cwHost: cwHost, inboxes: inboxes, ch: make(chan []byte, 32)}
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
	sc := b.scopeOf(sessionID)
	b.mu.RLock()
	defer b.mu.RUnlock()
	for s := range b.subs {
		if !s.wants(sc) {
			continue // widget de outra conta/caixa: não deve tocar/abrir
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
	sc := b.scopeOf(sessionID)
	b.mu.RLock()
	defer b.mu.RUnlock()
	for s := range b.subs {
		if !s.wants(sc) {
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

// emitWebRTCFailed avisa os painéis que o áudio do navegador não conectou numa
// chamada que continua de pé (o painel pode tentar /webrtc de novo).
func (b *Broker) emitWebRTCFailed(sessionID, id, reason string) {
	b.broadcastForSession(sessionID, map[string]any{"type": "call-webrtc-failed", "sessionId": sessionID, "id": id, "reason": reason})
}

func (b *Broker) emitAuthState(sessionID string, a AuthSnapshot) {
	ev := map[string]any{
		"type": "auth-state", "sessionId": sessionID,
		"paired": a.Paired, "state": a.State, "qr": a.QR,
	}
	if a.Code != "" {
		ev["code"] = a.Code // código de pareamento por telefone (8 dígitos)
	}
	if len(a.Passkey) > 0 {
		ev["passkey"] = a.Passkey // desafio WebAuthn p/ contas com passkey
	}
	b.broadcastForSession(sessionID, ev) // painel + widget/integração da conta dona
}

// emitSessionList manda a cada assinante só as sessões do escopo dele (o painel
// admin vê todas; o widget de uma conta, só as da própria conta/caixa).
func (b *Broker) emitSessionList(sessions []SessionInfo) {
	scopes := b.sessionScopes(sessions)
	b.mu.RLock()
	defer b.mu.RUnlock()
	for s := range b.subs {
		data, err := json.Marshal(sessionListFor(s, sessions, scopes))
		if err != nil {
			return
		}
		select {
		case s.ch <- data:
		default:
		}
	}
}

// sessionScopes resolve o escopo de cada sessão. Fora do b.mu: scopeOf consulta o
// SessionManager, que tem lock próprio.
func (b *Broker) sessionScopes(sessions []SessionInfo) map[string]sessionScope {
	scopes := make(map[string]sessionScope, len(sessions))
	for _, si := range sessions {
		scopes[si.ID] = b.scopeOf(si.ID)
	}
	return scopes
}

func sessionListFor(s *subscriber, sessions []SessionInfo, scopes map[string]sessionScope) map[string]any {
	if s.accountID == 0 {
		return map[string]any{"type": "session-list", "sessions": sessions}
	}
	out := make([]SessionInfo, 0, len(sessions))
	for _, si := range sessions {
		if s.wants(scopes[si.ID]) {
			out = append(out, si)
		}
	}
	return map[string]any{"type": "session-list", "sessions": out}
}

// QR/pareamento: painel admin e a conta dona da sessão (o AstraChat pareia por
// ela); nunca widgets de outras contas, que poderiam parear o número.
func (b *Broker) emitSessionQR(sessionID, qr string) {
	b.broadcastForSession(sessionID, map[string]any{"type": "session-qr", "sessionId": sessionID, "qr": qr})
}

func (b *Broker) upsertCall(r CallRecord) {
	b.mu.Lock()
	cp := r
	b.calls[r.CallID] = &cp
	b.mu.Unlock()
	b.broadcastCallList()
	b.broadcastForSession(r.SessionID, map[string]any{
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
	b.broadcastForSession(rec.SessionID, map[string]any{
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

	b.broadcastForSession(sessionID, map[string]any{
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
	// cada assinante recebe só as chamadas das sessões do seu escopo (escopo
	// resolvido fora do b.mu: scopeOf consulta o SessionManager).
	scopes := map[string]sessionScope{}
	for _, c := range list {
		if _, ok := scopes[c.SessionID]; !ok {
			scopes[c.SessionID] = b.scopeOf(c.SessionID)
		}
	}
	b.mu.RLock()
	for s := range b.subs {
		mine := list
		if s.accountID != 0 {
			mine = make([]CallRecord, 0, len(list))
			for _, c := range list {
				if s.wants(scopes[c.SessionID]) {
					mine = append(mine, c)
				}
			}
		}
		data, err := json.Marshal(map[string]any{"type": "call-list", "calls": mine})
		if err != nil {
			break
		}
		select {
		case s.ch <- data:
		default:
		}
	}
	b.mu.RUnlock()
}

func (b *Broker) emitIncoming(sessionID, id, peer, phone, name string, video bool) {
	// escopado por conta: só toca no widget da empresa dona da sessão.
	// phone/name resolvidos (issue #9): o widget mostra o número/nome em vez do
	// LID cru; peer segue no payload por compatibilidade.
	b.broadcastForSession(sessionID, map[string]any{
		"type": "incoming", "sessionId": sessionID, "id": id, "peer": peer,
		"phone": phone, "name": name,
		"video": video, "offeredAt": time.Now().UnixMilli(), "source": "whatsapp",
	})
}

// emitIncomingSIP é o "incoming" de uma ligação SIP atendida pelo modo agente:
// mesmo formato da chamada WhatsApp (peer = phone = número do chamador) + source
// "sip" e o bloco sip {callId, from, to, codec}.
func (b *Broker) emitIncomingSIP(sessionID, id, phone, name string, sipInfo map[string]any) {
	b.broadcastForSession(sessionID, map[string]any{
		"type": "incoming", "sessionId": sessionID, "id": id, "peer": phone,
		"phone": phone, "name": name, "video": false, "offeredAt": time.Now().UnixMilli(),
		"source": "sip", "sip": sipInfo,
	})
}

// emitVideoState avisa a UI sobre negociação de vídeo mid-call (pedido de upgrade,
// câmera do peer ligada/desligada, etc.). Escopado por conta como as chamadas.
func (b *Broker) emitVideoState(sessionID, id, kind string, peerVideo, localVideo, upgradeIncoming, upgradeOutgoing bool, peerOrientation int) {
	b.broadcastForSession(sessionID, map[string]any{
		"type": "video-state", "sessionId": sessionID, "id": id, "kind": kind,
		"peerVideo": peerVideo, "localVideo": localVideo,
		"upgradeIncoming": upgradeIncoming, "upgradeOutgoing": upgradeOutgoing,
		"peerOrientation": peerOrientation, // rotação da câmera do peer (0..3, quartos de volta)
	})
}

// emitCallAction avisa o painel/widget de uma ação mid-call portada do zapo
// (kind: "peer-mute" | "peer-hand"), com o estado booleano anunciado pelo peer.
func (b *Broker) emitCallAction(sessionID, id, kind string, state bool) {
	b.broadcastForSession(sessionID, map[string]any{
		"type": "call-action", "sessionId": sessionID, "id": id, "kind": kind, "state": state,
	})
}

// subscriberScope devolve, para diagnóstico, o account_id resolvido da sessão e a
// contagem de assinantes: total, quantos receberão eventos escopados por conta
// (escopo casa) e quantos são widgets de OUTRA conta (que serão filtrados). Serve
// para explicar por que uma chamada recebida "toca no painel mas não no widget".
func (b *Broker) subscriberScope(sessionID string) (acct, total, matched, widgetOtherAcct int) {
	sc := b.scopeOf(sessionID)
	acct = sc.acct
	b.mu.RLock()
	defer b.mu.RUnlock()
	for s := range b.subs {
		total++
		if !s.wants(sc) {
			widgetOtherAcct++
			continue
		}
		matched++
	}
	return
}

// emitCallVolume avisa as UIs que o ganho do áudio da chamada mudou (POST /volume),
// para o controle de volume ficar sincronizado entre painel/widgets.
func (b *Broker) emitCallVolume(sessionID, id string, level float64) {
	b.broadcastForSession(sessionID, map[string]any{
		"type": "call-volume", "sessionId": sessionID, "id": id, "level": level,
	})
}

// emitCallSpy avisa que alguém começou/parou de espiar a chamada (count = nº de
// ouvintes). Mesmo formato do call-action (kind=spy, state = tem alguém ouvindo).
func (b *Broker) emitCallSpy(sessionID, id string, count int) {
	b.broadcastForSession(sessionID, map[string]any{
		"type": "call-action", "sessionId": sessionID, "id": id, "kind": "spy", "state": count > 0, "count": count,
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

func (b *Broker) serveSSE(w http.ResponseWriter, r *http.Request, clientID string, accountID int, cwHost string, inboxes map[int]bool) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	sub := b.subscribeInboxes(clientID, accountID, cwHost, inboxes)
	defer b.unsubscribe(sub)

	if b.SessionsFn != nil {
		sessions := b.SessionsFn()
		writeSSE(w, flusher, sessionListFor(sub, sessions, b.sessionScopes(sessions)))
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

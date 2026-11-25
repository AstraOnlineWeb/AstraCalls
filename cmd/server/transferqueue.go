package main

import (
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

// Fila de transferência (ring-next).
//
// Uma transferência sem `to` (para a fila) não é mais oferecida a todos ao mesmo
// tempo: entra numa fila FIFO por sessão e é oferecida a UM atendente livre por
// vez (o que está há mais tempo sem receber chamada da fila). Se ele não assumir
// em ringSeconds (padrão 20 s) ou recusar (POST .../transfer/decline), a oferta
// é retirada (call-transfer-withdrawn) e vai para o próximo. A chamada mais
// antiga na fila é oferecida primeiro, e cada atendente só recebe uma oferta da
// fila por vez. Enquanto espera, a chamada fica em espera com música.
//
// Só quem está com a oferta consegue assumir (/pickup): acaba a corrida de dois
// atendentes pegando a mesma chamada. Transferência direcionada (`to`) também
// trava: só o alvo assume. mode:"all" mantém o comportamento antigo (oferta a
// todos, o primeiro /pickup leva — ainda com trava contra o segundo).

const (
	queueDefaultRing = 20 * time.Second
	queueMinRing     = 5 * time.Second
	queueMaxRing     = 120 * time.Second
	queueTick        = time.Second
)

type queueEntry struct {
	sessionID, callID, peer string
	from, fromName          string
	mode                    string // "queue" | "direct" | "all"
	target                  string // oferta atual (queue) ou alvo fixo (direct)
	offeredAt               time.Time
	enqueuedAt              time.Time
	ring                    time.Duration
	tried                   map[string]bool
	attempts                int
	lastPos                 int
}

type transferQueue struct {
	s        *server
	mu       sync.Mutex
	entries  []*queueEntry        // ordem de chegada
	lastGot  map[string]time.Time // clientId → última oferta da fila recebida
	running  bool
	idleStop chan struct{}
}

// transfers devolve a fila de transferências (criada na primeira vez).
func (s *server) transfers() *transferQueue {
	s.queueOnce.Do(func() {
		if s.queue == nil {
			s.queue = newTransferQueue(s)
		}
	})
	return s.queue
}

func newTransferQueue(s *server) *transferQueue {
	return &transferQueue{s: s, lastGot: map[string]time.Time{}}
}

func (q *transferQueue) find(callID string) (int, *queueEntry) {
	for i, e := range q.entries {
		if e.callID == callID {
			return i, e
		}
	}
	return -1, nil
}

// enqueue registra a transferência (substitui uma anterior da mesma chamada).
func (q *transferQueue) enqueue(e *queueEntry) {
	e.enqueuedAt = time.Now()
	e.tried = map[string]bool{}
	q.mu.Lock()
	if i, old := q.find(e.callID); old != nil {
		q.entries = append(q.entries[:i], q.entries[i+1:]...)
		if old.mode == "queue" && old.target != "" {
			defer q.withdraw(old, old.target, "retransferida")
		}
	}
	q.entries = append(q.entries, e)
	start := !q.running && e.mode == "queue"
	if start {
		q.running = true
	}
	q.mu.Unlock()
	switch e.mode {
	case "direct":
		q.s.broker.emitTransferOfferEx(e.sessionID, e.callID, e.peer, e.from, e.fromName, e.target, nil)
	case "all":
		q.s.broker.emitTransferOfferEx(e.sessionID, e.callID, e.peer, e.from, e.fromName, "", nil)
	}
	if start {
		go q.loop()
	} else if e.mode == "queue" {
		go q.dispatch()
	}
}

// claim tenta assumir a chamada para owner. ok=false + motivo quando a oferta
// está com outro atendente. Chamada fora da fila: livre (compatibilidade).
func (q *transferQueue) claim(callID, owner string) (bool, string) {
	q.mu.Lock()
	i, e := q.find(callID)
	if e == nil {
		q.mu.Unlock()
		return true, ""
	}
	switch e.mode {
	case "direct", "queue":
		if e.target != owner {
			q.mu.Unlock()
			if e.mode == "direct" {
				return false, "esta transferência é para outro atendente"
			}
			return false, "a chamada da fila está sendo oferecida a outro atendente"
		}
	}
	q.entries = append(q.entries[:i], q.entries[i+1:]...)
	if e.mode == "queue" {
		q.lastGot[owner] = time.Now()
	}
	q.mu.Unlock()
	go q.publishPositions(e.sessionID)
	return true, ""
}

// decline: o atendente com a oferta recusa → passa para o próximo.
func (q *transferQueue) decline(callID, who string) bool {
	q.mu.Lock()
	_, e := q.find(callID)
	if e == nil || e.mode != "queue" || e.target != who {
		q.mu.Unlock()
		return false
	}
	e.tried[who] = true
	e.target = ""
	q.lastGot[who] = time.Now()
	q.mu.Unlock()
	q.withdraw(e, who, "recusada")
	go q.dispatch()
	return true
}

// drop tira a chamada da fila (encerrada, assumida por outro caminho).
func (q *transferQueue) drop(callID, reason string) {
	q.mu.Lock()
	i, e := q.find(callID)
	if e != nil {
		q.entries = append(q.entries[:i], q.entries[i+1:]...)
	}
	q.mu.Unlock()
	if e != nil && e.mode == "queue" && e.target != "" {
		q.withdraw(e, e.target, reason)
	}
	if e != nil {
		q.publishPositions(e.sessionID)
	}
}

func (q *transferQueue) withdraw(e *queueEntry, target, reason string) {
	q.s.broker.broadcastForSessionTargeted(e.sessionID, target, map[string]any{
		"type": "call-transfer-withdrawn", "sessionId": e.sessionID, "id": e.callID, "to": target, "reason": reason,
	})
}

func (q *transferQueue) loop() {
	t := time.NewTicker(queueTick)
	defer t.Stop()
	for range t.C {
		if !q.dispatch() {
			q.mu.Lock()
			if !q.hasQueueLocked() {
				q.running = false
				q.mu.Unlock()
				return
			}
			q.mu.Unlock()
		}
	}
}

func (q *transferQueue) hasQueueLocked() bool {
	for _, e := range q.entries {
		if e.mode == "queue" {
			return true
		}
	}
	return false
}

type queueOffer struct {
	e      *queueEntry
	target string
	pos    int
}

// dispatch faz uma rodada: limpa chamadas encerradas, expira ofertas vencidas e
// oferece as chamadas sem oferta a atendentes livres, na ordem de chegada.
// Devolve se ainda há algo na fila.
func (q *transferQueue) dispatch() bool {
	now := time.Now()
	var gone []*queueEntry
	var expired []queueOffer
	q.mu.Lock()
	kept := q.entries[:0]
	for _, e := range q.entries {
		if cr, ok := q.s.broker.getCall(e.callID); !ok || cr == nil || cr.Status == StatusEnded || cr.Owner != nil {
			gone = append(gone, e)
			continue
		}
		kept = append(kept, e)
	}
	q.entries = kept
	reserved := map[string]bool{}
	for _, e := range q.entries {
		if e.mode != "queue" || e.target == "" {
			continue
		}
		if now.Sub(e.offeredAt) >= e.ring {
			expired = append(expired, queueOffer{e: e, target: e.target})
			e.tried[e.target] = true
			q.lastGot[e.target] = now
			e.target = ""
			continue
		}
		reserved[e.target] = true
	}
	// candidatos por sessão, calculados fora do lock do broker uma vez por rodada
	agentsBy := map[string][]agentInfo{}
	var offers []queueOffer
	pos := map[string]int{}
	for _, e := range q.entries {
		if e.mode != "queue" {
			continue
		}
		pos[e.sessionID]++
		p := pos[e.sessionID]
		if e.target != "" {
			continue
		}
		list, ok := agentsBy[e.sessionID]
		if !ok {
			list = q.s.broker.agentsFor(e.sessionID)
			agentsBy[e.sessionID] = list
		}
		cand := q.pickLocked(e, list, reserved)
		if cand == "" {
			continue
		}
		e.target, e.offeredAt = cand, now
		e.attempts++
		reserved[cand] = true
		offers = append(offers, queueOffer{e: e, target: cand, pos: p})
	}
	more := q.hasQueueLocked()
	q.mu.Unlock()

	for _, x := range expired {
		q.withdraw(x.e, x.target, "tempo esgotado")
	}
	for _, x := range offers {
		q.s.broker.emitTransferOfferEx(x.e.sessionID, x.e.callID, x.e.peer, x.e.from, x.e.fromName, x.target, map[string]any{
			"queue": true, "ringSeconds": int(x.e.ring / time.Second), "attempt": x.e.attempts,
			"position": x.pos, "waitingSince": x.e.enqueuedAt.UnixMilli(),
		})
	}
	sessions := map[string]bool{}
	for _, e := range gone {
		if e.mode == "queue" && e.target != "" {
			q.withdraw(e, e.target, "encerrada")
		}
		sessions[e.sessionID] = true
	}
	for sid := range pos {
		sessions[sid] = true
	}
	for sid := range sessions {
		q.publishPositions(sid)
	}
	return more
}

// pickLocked escolhe o próximo atendente: livre, conectado, não reservado, que
// ainda não recusou/deixou expirar nesta volta (zera a volta quando todos já
// foram tentados) e há mais tempo sem receber chamada da fila.
func (q *transferQueue) pickLocked(e *queueEntry, list []agentInfo, reserved map[string]bool) string {
	free := make([]agentInfo, 0, len(list))
	for _, a := range list {
		if a.Busy || reserved[a.ClientID] || a.ClientID == e.from {
			continue
		}
		free = append(free, a)
	}
	if len(free) == 0 {
		return ""
	}
	untried := free[:0:0]
	for _, a := range free {
		if !e.tried[a.ClientID] {
			untried = append(untried, a)
		}
	}
	if len(untried) == 0 { // todos já tentados: nova volta
		e.tried = map[string]bool{}
		untried = free
	}
	sort.SliceStable(untried, func(i, j int) bool {
		ti, tj := q.lastGot[untried[i].ClientID], q.lastGot[untried[j].ClientID]
		if !ti.Equal(tj) {
			return ti.Before(tj)
		}
		return untried[i].ConnectedAt < untried[j].ConnectedAt
	})
	return untried[0].ClientID
}

// publishPositions emite call-queue-position para as chamadas da fila da sessão
// cuja posição mudou.
func (q *transferQueue) publishPositions(sessionID string) {
	type upd struct {
		id       string
		pos, tot int
		target   string
	}
	var ups []upd
	q.mu.Lock()
	tot := 0
	for _, e := range q.entries {
		if e.sessionID == sessionID && e.mode == "queue" {
			tot++
		}
	}
	p := 0
	for _, e := range q.entries {
		if e.sessionID != sessionID || e.mode != "queue" {
			continue
		}
		p++
		if e.lastPos != p*1000+tot {
			e.lastPos = p*1000 + tot
			ups = append(ups, upd{e.callID, p, tot, e.target})
		}
	}
	q.mu.Unlock()
	for _, u := range ups {
		q.s.broker.broadcastForSession(sessionID, map[string]any{
			"type": "call-queue-position", "sessionId": sessionID, "id": u.id,
			"position": u.pos, "waiting": u.tot, "offeredTo": u.target,
		})
	}
}

// snapshot da fila de uma sessão (GET /calls/queue).
func (q *transferQueue) snapshot(sessionID string) []map[string]any {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := []map[string]any{}
	p := 0
	for _, e := range q.entries {
		if e.sessionID != sessionID {
			continue
		}
		item := map[string]any{
			"id": e.callID, "mode": e.mode, "peer": e.peer, "from": e.from, "fromName": e.fromName,
			"waitingSince": e.enqueuedAt.UnixMilli(), "offeredTo": e.target,
		}
		if e.mode == "queue" {
			p++
			item["position"] = p
			item["ringSeconds"] = int(e.ring / time.Second)
			item["attempts"] = e.attempts
			if e.target != "" {
				item["offeredAt"] = e.offeredAt.UnixMilli()
			}
		}
		out = append(out, item)
	}
	return out
}

// offerTransfer é o ponto único das transferências (WhatsApp e SIP agente).
// to != "" → direcionada; mode "all" → oferta a todos; senão → fila ring-next.
func (s *server) offerTransfer(sessionID, callID, peer, from, fromName, to, mode string, ringSeconds int) string {
	if fromName == "" {
		fromName = s.broker.labelOf(from)
	}
	e := &queueEntry{sessionID: sessionID, callID: callID, peer: peer, from: from, fromName: fromName}
	switch {
	case to != "":
		e.mode, e.target = "direct", to
	case strings.EqualFold(mode, "all"):
		e.mode = "all"
	default:
		e.mode = "queue"
		e.ring = queueDefaultRing
		if ringSeconds > 0 {
			e.ring = time.Duration(ringSeconds) * time.Second
		}
		if e.ring < queueMinRing {
			e.ring = queueMinRing
		}
		if e.ring > queueMaxRing {
			e.ring = queueMaxRing
		}
	}
	s.transfers().enqueue(e)
	return e.mode
}

// POST /calls/{id}/transfer/decline — o atendente com a oferta da fila recusa.
func (s *server) handleTransferDecline(w http.ResponseWriter, r *http.Request) {
	sess := s.sessionByID(w, r.PathValue("sid"))
	if sess == nil {
		return
	}
	if !s.transfers().decline(r.PathValue("id"), clientID(r)) {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "não há oferta da fila desta chamada para você"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// GET /calls/queue — chamadas aguardando atendente (fila + transferências pendentes).
func (s *server) handleTransferQueue(w http.ResponseWriter, r *http.Request) {
	sess := s.sessionByID(w, r.PathValue("sid"))
	if sess == nil {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"queue": s.transfers().snapshot(sess.id)})
}

// claimTransfer aplica as travas do pickup: oferta da fila/direcionada só para o
// alvo, e nunca dois donos. Escreve o erro e devolve false quando recusa.
func (s *server) claimTransfer(w http.ResponseWriter, callID, owner string) bool {
	if owner == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "clientId obrigatório (header X-Client-Id)"})
		return false
	}
	if ok, why := s.transfers().claim(callID, owner); !ok {
		writeJSON(w, http.StatusConflict, map[string]string{"error": why, "code": "offered_to_other"})
		return false
	}
	ok, exists := s.broker.claimOwner(callID, owner)
	if !exists {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such call"})
		return false
	}
	if !ok {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "a chamada já foi assumida por outro atendente", "code": "already_claimed"})
		return false
	}
	return true
}

package main

import (
	"encoding/json"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// Broker de eventos SSE no MESMO formato do wacalls (broker.go), para o Botfy
// (listener, ponte de voz e softphone) não precisar saber que do outro lado é SIP.
// Diferença deliberada: TODO evento é escopado pela conta da linha — no wacalls
// call-status/call-ended/call-list iam para todos os assinantes.
type CallRecord struct {
	SessionID  string  `json:"sessionId"`
	CallID     string  `json:"callId"`
	Owner      *string `json:"owner"`
	Direction  string  `json:"direction"`
	Peer       string  `json:"peer"`
	StartedAt  int64   `json:"startedAt"`
	Status     string  `json:"status"`
	Held       bool    `json:"held"`
	Extension  string  `json:"extension,omitempty"`
	AgentEmail string  `json:"agentEmail,omitempty"`
}

type subscriber struct {
	clientID  string
	accountID int // 0 = admin (recebe tudo)
	ch        chan []byte
}

type broker struct {
	mu   sync.Mutex
	subs map[*subscriber]struct{}
}

func newBroker() *broker {
	return &broker{subs: map[*subscriber]struct{}{}}
}

func (b *broker) subscribe(clientID string, accountID int) *subscriber {
	sub := &subscriber{clientID: clientID, accountID: accountID, ch: make(chan []byte, 64)}
	b.mu.Lock()
	b.subs[sub] = struct{}{}
	b.mu.Unlock()
	return sub
}

func (b *broker) unsubscribe(sub *subscriber) {
	b.mu.Lock()
	delete(b.subs, sub)
	b.mu.Unlock()
}

// emit entrega o evento a quem pode ver a conta. Assinante lento perde o evento
// em vez de travar a chamada (o próximo call-list reconcilia).
func (b *broker) emit(accountID int, ev any) {
	data, err := json.Marshal(ev)
	if err != nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for sub := range b.subs {
		if sub.accountID != 0 && sub.accountID != accountID {
			continue
		}
		select {
		case sub.ch <- data:
		default:
		}
	}
}

func (b *broker) serve(w http.ResponseWriter, r *http.Request, clientID string, accountID int, snapshot []any) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	sub := b.subscribe(clientID, accountID)
	defer b.unsubscribe(sub)

	for _, ev := range snapshot {
		data, _ := json.Marshal(ev)
		_, _ = w.Write(append(append([]byte("data: "), data...), '\n', '\n'))
	}
	flusher.Flush()

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
			_, _ = w.Write([]byte(": ping\n\n"))
			flusher.Flush()
		}
	}
}

// GET /api/events?clientId=&accountId= — token de widget fica preso à própria conta.
func (s *server) handleEvents(w http.ResponseWriter, r *http.Request, p principal) {
	cid := clientID(r)
	acc := p.accountID
	if p.admin {
		acc, _ = strconv.Atoi(r.URL.Query().Get("accountId"))
		if acc == 0 {
			acc = accountFromClientID(cid)
		}
	}
	s.log.Info("sse connect", "clientId", cid, "accountId", acc)
	s.broker.serve(w, r, cid, acc, []any{s.calls.listEvent(acc)})
	s.log.Info("sse disconnect", "clientId", cid, "accountId", acc)
}

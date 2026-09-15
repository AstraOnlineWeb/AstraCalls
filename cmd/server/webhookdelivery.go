package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

var (
	errWebhookDisabled = errors.New("webhook disabled")
	errWebhookOpen     = errors.New("webhook circuit is OPEN")
	errDLQNotFound     = errors.New("dlq item not found")
)

// Entrega confiável de webhook de sessão. Melhora o POST simples com:
//   - RETRY com backoff escalonado (0 -> 5s -> 30s), até whMaxAttempts.
//   - CIRCUIT BREAKER por sessão: após whOpenThreshold falhas consecutivas o
//     circuito ABRE por whCooldown (não bate no endpoint morto); após
//     whHardDisable falhas, desabilita até religar manualmente.
//   - DLQ (dead-letter queue) por sessão: os eventos que falharam em todas as
//     tentativas ficam num ring limitado (whDLQCapacity), consultável e
//     reenviável por endpoint.
//
// O PAYLOAD e os HEADERS enviados são idênticos ao POST antigo — isto é só
// robustez de entrega; nada muda pra quem consome (ex.: AstraChat).
const (
	whMaxAttempts   = 3
	whOpenThreshold = 25
	whHardDisable   = 100
	whCooldown      = 300 * time.Second
	whDLQCapacity   = 100
)

// backoff das tentativas (índice = nº da tentativa já feita). A 1ª é imediata.
var whBackoff = []time.Duration{0, 5 * time.Second, 30 * time.Second}

type whDLQItem struct {
	ID        string          `json:"id"`
	Event     string          `json:"event"`
	URL       string          `json:"url"`
	Body      json.RawMessage `json:"body"`
	LastError string          `json:"lastError"`
	Attempts  int             `json:"attempts"`
	CreatedAt int64           `json:"createdAt"`
}

type whCircuit struct {
	consecFails    int
	openUntil      time.Time
	disabled       bool
	disabledReason string
}

type webhookDeliverer struct {
	mu       sync.Mutex
	circuits map[string]*whCircuit
	dlq      map[string][]whDLQItem
	client   *http.Client
	log      *slog.Logger
}

var whDeliver = &webhookDeliverer{
	circuits: map[string]*whCircuit{},
	dlq:      map[string][]whDLQItem{},
	client:   &http.Client{Timeout: 10 * time.Second},
	log:      slog.Default(),
}

func whNewID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// deliver entrega (assíncrono) um evento já serializado à URL da sessão, com
// retry/circuit breaker/DLQ. sessionID escopa o circuito e a DLQ.
func (d *webhookDeliverer) deliver(sessionID, url, event string, body []byte) {
	d.mu.Lock()
	c := d.circuits[sessionID]
	if c == nil {
		c = &whCircuit{}
		d.circuits[sessionID] = c
	}
	if c.disabled {
		d.mu.Unlock()
		d.pushDLQ(sessionID, url, event, body, "webhook disabled: "+c.disabledReason, 0)
		return
	}
	if time.Now().Before(c.openUntil) {
		d.mu.Unlock()
		d.pushDLQ(sessionID, url, event, body, "webhook circuit is OPEN", 0)
		return
	}
	d.mu.Unlock()

	go d.attempt(sessionID, url, event, body)
}

func (d *webhookDeliverer) attempt(sessionID, url, event string, body []byte) {
	var lastErr string
	for i := 0; i < whMaxAttempts; i++ {
		if delay := whBackoff[i]; delay > 0 {
			time.Sleep(delay)
		}
		code, err := d.post(url, body)
		if err == nil && code >= 200 && code < 300 {
			d.onSuccess(sessionID)
			return
		}
		if err != nil {
			lastErr = err.Error()
		} else {
			lastErr = http.StatusText(code)
		}
	}
	d.onFailure(sessionID, url, event, body, lastErr)
}

func (d *webhookDeliverer) post(url string, body []byte) (int, error) {
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := d.client.Do(req)
	if err != nil {
		return 0, err
	}
	_ = resp.Body.Close()
	return resp.StatusCode, nil
}

func (d *webhookDeliverer) onSuccess(sessionID string) {
	d.mu.Lock()
	if c := d.circuits[sessionID]; c != nil {
		c.consecFails = 0
		c.openUntil = time.Time{}
	}
	d.mu.Unlock()
}

func (d *webhookDeliverer) onFailure(sessionID, url, event string, body []byte, lastErr string) {
	d.mu.Lock()
	c := d.circuits[sessionID]
	if c == nil {
		c = &whCircuit{}
		d.circuits[sessionID] = c
	}
	c.consecFails++
	fails := c.consecFails
	switch {
	case fails >= whHardDisable:
		c.disabled = true
		c.disabledReason = "too many consecutive failures"
	case fails >= whOpenThreshold:
		c.openUntil = time.Now().Add(whCooldown)
	}
	d.mu.Unlock()
	if d.log != nil {
		d.log.Warn("webhook: entrega falhou (foi pra DLQ)", "session", sessionID, "event", event, "consecFails", fails, "err", lastErr)
	}
	d.pushDLQ(sessionID, url, event, body, lastErr, whMaxAttempts)
}

func (d *webhookDeliverer) pushDLQ(sessionID, url, event string, body []byte, lastErr string, attempts int) {
	item := whDLQItem{
		ID: whNewID(), Event: event, URL: url,
		Body: append(json.RawMessage(nil), body...), LastError: lastErr,
		Attempts: attempts, CreatedAt: time.Now().UnixMilli(),
	}
	d.mu.Lock()
	q := d.dlq[sessionID]
	q = append([]whDLQItem{item}, q...) // newest-first
	if len(q) > whDLQCapacity {
		q = q[:whDLQCapacity] // evita o mais antigo (cauda)
	}
	d.dlq[sessionID] = q
	d.mu.Unlock()
}

// dlqList devolve a DLQ da sessão (cópia).
func (d *webhookDeliverer) dlqList(sessionID string) []whDLQItem {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]whDLQItem, len(d.dlq[sessionID]))
	copy(out, d.dlq[sessionID])
	return out
}

// dlqReplay reenvia um item da DLQ (remove-o da fila e re-entrega). Erro se o
// circuito estiver aberto/desabilitado ou o id não existir.
func (d *webhookDeliverer) dlqReplay(sessionID, id string) error {
	d.mu.Lock()
	c := d.circuits[sessionID]
	if c != nil && c.disabled {
		d.mu.Unlock()
		return errWebhookDisabled
	}
	if c != nil && time.Now().Before(c.openUntil) {
		d.mu.Unlock()
		return errWebhookOpen
	}
	q := d.dlq[sessionID]
	idx := -1
	for i := range q {
		if q[i].ID == id {
			idx = i
			break
		}
	}
	if idx < 0 {
		d.mu.Unlock()
		return errDLQNotFound
	}
	item := q[idx]
	d.dlq[sessionID] = append(q[:idx:idx], q[idx+1:]...)
	d.mu.Unlock()
	d.deliver(sessionID, item.URL, item.Event, item.Body)
	return nil
}

// status devolve o estado do circuito da sessão.
func (d *webhookDeliverer) status(sessionID string) map[string]any {
	d.mu.Lock()
	defer d.mu.Unlock()
	c := d.circuits[sessionID]
	st := map[string]any{"dlqSize": len(d.dlq[sessionID])}
	if c == nil {
		st["state"] = "closed"
		st["consecutiveFailures"] = 0
		return st
	}
	state := "closed"
	if c.disabled {
		state = "disabled"
	} else if time.Now().Before(c.openUntil) {
		state = "open"
	}
	st["state"] = state
	st["consecutiveFailures"] = c.consecFails
	if c.disabledReason != "" {
		st["disabledReason"] = c.disabledReason
	}
	if !c.openUntil.IsZero() && time.Now().Before(c.openUntil) {
		st["openUntil"] = c.openUntil.UnixMilli()
	}
	return st
}

// reenable religa um webhook desabilitado/aberto (zera o circuito).
func (d *webhookDeliverer) reenable(sessionID string) {
	d.mu.Lock()
	if c := d.circuits[sessionID]; c != nil {
		c.disabled = false
		c.disabledReason = ""
		c.consecFails = 0
		c.openUntil = time.Time{}
	}
	d.mu.Unlock()
}

// ---------- endpoints ----------

// GET /api/sessions/{sid}/webhooks/status
func (s *server) handleWebhookStatus(w http.ResponseWriter, r *http.Request) {
	sess := s.sessionByID(w, r.PathValue("sid"))
	if sess == nil {
		return
	}
	writeJSON(w, http.StatusOK, whDeliver.status(sess.id))
}

// GET /api/sessions/{sid}/webhooks/dlq
func (s *server) handleWebhookDLQ(w http.ResponseWriter, r *http.Request) {
	sess := s.sessionByID(w, r.PathValue("sid"))
	if sess == nil {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": whDeliver.dlqList(sess.id)})
}

// POST /api/sessions/{sid}/webhooks/dlq/{id}/replay
func (s *server) handleWebhookDLQReplay(w http.ResponseWriter, r *http.Request) {
	sess := s.sessionByID(w, r.PathValue("sid"))
	if sess == nil {
		return
	}
	if err := whDeliver.dlqReplay(sess.id, r.PathValue("id")); err != nil {
		code := http.StatusConflict
		if err == errDLQNotFound {
			code = http.StatusNotFound
		}
		writeJSON(w, code, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "replayed"})
}

// POST /api/sessions/{sid}/webhooks/reenable
func (s *server) handleWebhookReenable(w http.ResponseWriter, r *http.Request) {
	sess := s.sessionByID(w, r.PathValue("sid"))
	if sess == nil {
		return
	}
	whDeliver.reenable(sess.id)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

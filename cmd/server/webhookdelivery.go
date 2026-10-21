package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
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
//   - CIRCUIT BREAKER por webhook (sessão + id do webhook): após whOpenThreshold falhas consecutivas o
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
	WebhookID string          `json:"webhookId"`
	Event     string          `json:"event"`
	URL       string          `json:"url"`
	Body      json.RawMessage `json:"body"`
	LastError string          `json:"lastError"`
	Attempts  int             `json:"attempts"`
	CreatedAt int64           `json:"createdAt"`
	secret    string          // não serializado (não vaza na listagem da DLQ); usado no replay p/ reassinar
}

// signBody devolve a assinatura HMAC-SHA256 do corpo no formato "sha256=<hex>"
// (mesmo formato do GitHub/waxum), ou "" se não houver secret configurado.
func signBody(secret string, body []byte) string {
	if secret == "" {
		return ""
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
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

// whTarget é o destino de uma entrega: um webhook de uma sessão. O circuito é
// por webhook (um endpoint fora do ar não trava os outros da mesma sessão); a
// DLQ continua por sessão, com o webhookId em cada item.
type whTarget struct {
	session, hook, url, secret string
}

func (t whTarget) key() string { return whKey(t.session, t.hook) }

func whKey(sessionID, hookID string) string { return sessionID + "#" + hookID }

func whNewID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// deliver entrega (assíncrono) um evento já serializado ao webhook, com
// retry/circuit breaker/DLQ.
func (d *webhookDeliverer) deliver(t whTarget, event string, body []byte) {
	d.mu.Lock()
	c := d.circuits[t.key()]
	if c == nil {
		c = &whCircuit{}
		d.circuits[t.key()] = c
	}
	if c.disabled {
		d.mu.Unlock()
		d.pushDLQ(t, event, body, "webhook disabled: "+c.disabledReason, 0)
		return
	}
	if time.Now().Before(c.openUntil) {
		d.mu.Unlock()
		d.pushDLQ(t, event, body, "webhook circuit is OPEN", 0)
		return
	}
	d.mu.Unlock()

	go d.attempt(t, event, body)
}

func (d *webhookDeliverer) attempt(t whTarget, event string, body []byte) {
	var lastErr string
	for i := 0; i < whMaxAttempts; i++ {
		if delay := whBackoff[i]; delay > 0 {
			time.Sleep(delay)
		}
		code, err := d.post(t.url, body, t.secret)
		if err == nil && code >= 200 && code < 300 {
			d.onSuccess(t)
			return
		}
		if err != nil {
			lastErr = err.Error()
		} else {
			lastErr = http.StatusText(code)
		}
	}
	d.onFailure(t, event, body, lastErr)
}

func (d *webhookDeliverer) post(url string, body []byte, secret string) (int, error) {
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	if sig := signBody(secret, body); sig != "" {
		req.Header.Set("X-Webhook-Signature", sig)
	}
	resp, err := d.client.Do(req)
	if err != nil {
		return 0, err
	}
	_ = resp.Body.Close()
	return resp.StatusCode, nil
}

func (d *webhookDeliverer) onSuccess(t whTarget) {
	d.mu.Lock()
	if c := d.circuits[t.key()]; c != nil {
		c.consecFails = 0
		c.openUntil = time.Time{}
	}
	d.mu.Unlock()
}

func (d *webhookDeliverer) onFailure(t whTarget, event string, body []byte, lastErr string) {
	d.mu.Lock()
	c := d.circuits[t.key()]
	if c == nil {
		c = &whCircuit{}
		d.circuits[t.key()] = c
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
		d.log.Warn("webhook: entrega falhou (foi pra DLQ)", "session", t.session, "webhook", t.hook, "event", event, "consecFails", fails, "err", lastErr)
	}
	d.pushDLQ(t, event, body, lastErr, whMaxAttempts)
}

func (d *webhookDeliverer) pushDLQ(t whTarget, event string, body []byte, lastErr string, attempts int) {
	item := whDLQItem{
		ID: whNewID(), WebhookID: t.hook, Event: event, URL: t.url,
		Body: append(json.RawMessage(nil), body...), LastError: lastErr,
		Attempts: attempts, CreatedAt: time.Now().UnixMilli(), secret: t.secret,
	}
	d.mu.Lock()
	q := d.dlq[t.session]
	q = append([]whDLQItem{item}, q...) // newest-first
	if len(q) > whDLQCapacity {
		q = q[:whDLQCapacity] // evita o mais antigo (cauda)
	}
	d.dlq[t.session] = q
	d.mu.Unlock()
}

// totalDLQ soma os itens em DLQ de todas as sessões (métrica).
func (d *webhookDeliverer) totalDLQ() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	n := 0
	for _, q := range d.dlq {
		n += len(q)
	}
	return n
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
// circuito do webhook estiver aberto/desabilitado ou o id não existir.
func (d *webhookDeliverer) dlqReplay(sessionID, id string) error {
	d.mu.Lock()
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
	if c := d.circuits[whKey(sessionID, item.WebhookID)]; c != nil {
		if c.disabled {
			d.mu.Unlock()
			return errWebhookDisabled
		}
		if time.Now().Before(c.openUntil) {
			d.mu.Unlock()
			return errWebhookOpen
		}
	}
	d.dlq[sessionID] = append(q[:idx:idx], q[idx+1:]...)
	d.mu.Unlock()
	d.deliver(whTarget{session: sessionID, hook: item.WebhookID, url: item.URL, secret: item.secret}, item.Event, item.Body)
	return nil
}

// circuitState devolve o estado de um circuito (chamar com d.mu travado).
func (d *webhookDeliverer) circuitState(key string) map[string]any {
	c := d.circuits[key]
	st := map[string]any{"state": "closed", "consecutiveFailures": 0}
	if c == nil {
		return st
	}
	if c.disabled {
		st["state"] = "disabled"
	} else if time.Now().Before(c.openUntil) {
		st["state"] = "open"
	}
	st["consecutiveFailures"] = c.consecFails
	if c.disabledReason != "" {
		st["disabledReason"] = c.disabledReason
	}
	if !c.openUntil.IsZero() && time.Now().Before(c.openUntil) {
		st["openUntil"] = c.openUntil.UnixMilli()
	}
	return st
}

var whStateRank = map[string]int{"closed": 0, "open": 1, "disabled": 2}

// status devolve o estado dos circuitos da sessão. Os campos do topo (state,
// consecutiveFailures, ...) são os do PIOR webhook — compatível com o formato
// antigo (1 webhook por sessão); "webhooks" traz o estado de cada um.
func (d *webhookDeliverer) status(sessionID string, hookIDs []string) map[string]any {
	d.mu.Lock()
	defer d.mu.Unlock()
	st := map[string]any{"state": "closed", "consecutiveFailures": 0}
	per := make([]map[string]any, 0, len(hookIDs))
	for _, h := range hookIDs {
		cs := d.circuitState(whKey(sessionID, h))
		if whStateRank[cs["state"].(string)] > whStateRank[st["state"].(string)] ||
			(cs["state"] == st["state"] && cs["consecutiveFailures"].(int) > st["consecutiveFailures"].(int)) {
			for k, v := range cs {
				st[k] = v
			}
		}
		cs["id"] = h
		per = append(per, cs)
	}
	st["webhooks"] = per
	st["dlqSize"] = len(d.dlq[sessionID])
	return st
}

// hookState devolve o estado do circuito de um webhook.
func (d *webhookDeliverer) hookState(sessionID, hookID string) map[string]any {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.circuitState(whKey(sessionID, hookID))
}

// reenable religa webhooks desabilitados/abertos (zera o circuito). hookID
// vazio = todos os webhooks da sessão.
func (d *webhookDeliverer) reenable(sessionID, hookID string) {
	d.mu.Lock()
	for k, c := range d.circuits {
		if (hookID == "" && strings.HasPrefix(k, sessionID+"#")) || k == whKey(sessionID, hookID) {
			c.disabled = false
			c.disabledReason = ""
			c.consecFails = 0
			c.openUntil = time.Time{}
		}
	}
	d.mu.Unlock()
}

// forget descarta o circuito de um webhook removido.
func (d *webhookDeliverer) forget(sessionID, hookID string) {
	d.mu.Lock()
	delete(d.circuits, whKey(sessionID, hookID))
	d.mu.Unlock()
}

// ---------- endpoints ----------

// GET /api/sessions/{sid}/webhooks/status
func (s *server) handleWebhookStatus(w http.ResponseWriter, r *http.Request) {
	sess := s.sessionByID(w, r.PathValue("sid"))
	if sess == nil {
		return
	}
	writeJSON(w, http.StatusOK, whDeliver.status(sess.id, sess.webhookIDs()))
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

// POST /api/sessions/{sid}/webhooks/reenable[?webhookId=…] (sem id = todos)
func (s *server) handleWebhookReenable(w http.ResponseWriter, r *http.Request) {
	sess := s.sessionByID(w, r.PathValue("sid"))
	if sess == nil {
		return
	}
	whDeliver.reenable(sess.id, r.URL.Query().Get("webhookId"))
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

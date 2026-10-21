package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
)

// Vários webhooks por sessão. Cada um tem URL, secret, filtro de eventos e
// "ignorar grupos" próprios, e circuito/retry independentes (whTarget). O de id
// "default" é o mesmo da rota antiga POST/GET/DELETE /webhook — quem já usa a API
// de 1 webhook continua igual.

const (
	webhookDefaultID = "default"
	webhookMax       = 10
)

type WebhookTarget struct {
	ID           string   `json:"id"`
	Name         string   `json:"name,omitempty"`
	URL          string   `json:"url"`
	Secret       string   `json:"secret,omitempty"` // nunca vai pra resposta da API (ver view)
	Events       []string `json:"events,omitempty"` // vazio = todos
	IgnoreGroups bool     `json:"ignoreGroups,omitempty"`
}

// wants diz se o evento passa pelo filtro deste webhook.
func (t WebhookTarget) wants(event string, data any) bool {
	if t.IgnoreGroups && isGroupWebhookEvent(event, data) {
		return false
	}
	if len(t.Events) == 0 {
		return true
	}
	for _, e := range t.Events {
		if e == event {
			return true
		}
	}
	return false
}

// view é a forma pública (sem o secret).
func (t WebhookTarget) view() map[string]any {
	ev := t.Events
	if ev == nil {
		ev = []string{}
	}
	return map[string]any{
		"id": t.ID, "name": t.Name, "url": t.URL, "hasSecret": t.Secret != "",
		"events": ev, "ignoreGroups": t.IgnoreGroups,
	}
}

func (s *Session) getWebhooks() []WebhookTarget {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]WebhookTarget(nil), s.webhooks...)
}

func (s *Session) webhookIDs() []string {
	hooks := s.getWebhooks()
	ids := make([]string, len(hooks))
	for i, h := range hooks {
		ids[i] = h.ID
	}
	return ids
}

// webhookByID devolve o webhook pelo id (ok=false se não existe).
func (s *Session) webhookByID(id string) (WebhookTarget, bool) {
	for _, h := range s.getWebhooks() {
		if h.ID == id {
			return h, true
		}
	}
	return WebhookTarget{}, false
}

// loadWebhooks carrega a lista do banco; sem lista salva, usa o webhook legado.
func (s *Session) loadWebhooks(row sessionRow) {
	var list []WebhookTarget
	if row.Webhooks != "" {
		_ = json.Unmarshal([]byte(row.Webhooks), &list)
	} else if row.Webhook != "" {
		list = []WebhookTarget{{
			ID: webhookDefaultID, URL: row.Webhook, Secret: row.WebhookSecret,
			Events: splitEvents(row.WebhookEvents), IgnoreGroups: row.WebhookIgnoreGroups,
		}}
	}
	s.mu.Lock()
	s.webhooks = list
	s.mu.Unlock()
}

var (
	errWebhookNotFound = errors.New("webhook not found")
	errWebhookLimit    = errors.New("limite de webhooks por sessão atingido")
)

// updateWebhooks aplica fn na lista e persiste (serializado por sessão).
func (s *Session) updateWebhooks(ctx context.Context, fn func([]WebhookTarget) ([]WebhookTarget, error)) error {
	s.webhookMu.Lock()
	defer s.webhookMu.Unlock()
	list, err := fn(s.getWebhooks())
	if err != nil {
		return err
	}
	if len(list) > webhookMax {
		return errWebhookLimit
	}
	s.mu.Lock()
	s.webhooks = list
	s.mu.Unlock()

	var def WebhookTarget
	for _, h := range list {
		if h.ID == webhookDefaultID {
			def = h
		}
	}
	raw, _ := json.Marshal(list)
	if s.mgr == nil || s.mgr.store == nil {
		return nil
	}
	return s.mgr.store.setWebhooks(ctx, s.id, string(raw), def.URL, def.Secret, strings.Join(def.Events, ","), def.IgnoreGroups)
}

// webhookInput é o corpo de criar/alterar. Campos ausentes (nil) mantêm o atual
// na alteração; secret "" remove.
type webhookInput struct {
	Name         *string  `json:"name"`
	URL          *string  `json:"url"`
	Secret       *string  `json:"secret"`
	Events       []string `json:"events"`
	IgnoreGroups *bool    `json:"ignoreGroups"`
}

func (in webhookInput) apply(t *WebhookTarget, eventsSet bool) {
	if in.Name != nil {
		t.Name = strings.TrimSpace(*in.Name)
	}
	if in.URL != nil {
		t.URL = strings.TrimSpace(*in.URL)
	}
	if in.Secret != nil {
		t.Secret = strings.TrimSpace(*in.Secret)
	}
	if eventsSet {
		t.Events = cleanEvents(in.Events)
	}
	if in.IgnoreGroups != nil {
		t.IgnoreGroups = *in.IgnoreGroups
	}
}

func cleanEvents(in []string) []string {
	out := make([]string, 0, len(in))
	for _, e := range in {
		if e = strings.TrimSpace(e); e != "" {
			out = append(out, e)
		}
	}
	return out
}

func validWebhookURL(u string) bool {
	return strings.HasPrefix(u, "http://") || strings.HasPrefix(u, "https://")
}

// decodeWebhookInput lê o corpo e diz se "events" veio no JSON.
func decodeWebhookInput(r *http.Request) (webhookInput, bool, error) {
	var raw map[string]json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
		return webhookInput{}, false, err
	}
	b, _ := json.Marshal(raw)
	var in webhookInput
	if err := json.Unmarshal(b, &in); err != nil {
		return webhookInput{}, false, err
	}
	_, eventsSet := raw["events"]
	return in, eventsSet, nil
}

func webhookViewWithState(sid string, t WebhookTarget) map[string]any {
	v := t.view()
	v["status"] = whDeliver.hookState(sid, t.ID)
	return v
}

// GET /api/sessions/{sid}/webhooks
func (s *server) handleListWebhooks(w http.ResponseWriter, r *http.Request) {
	sess := s.sessionByID(w, r.PathValue("sid"))
	if sess == nil {
		return
	}
	hooks := sess.getWebhooks()
	out := make([]map[string]any, 0, len(hooks))
	for _, h := range hooks {
		out = append(out, webhookViewWithState(sess.id, h))
	}
	writeJSON(w, http.StatusOK, map[string]any{"webhooks": out, "max": webhookMax})
}

// POST /api/sessions/{sid}/webhooks
func (s *server) handleCreateWebhook(w http.ResponseWriter, r *http.Request) {
	sess := s.sessionByID(w, r.PathValue("sid"))
	if sess == nil {
		return
	}
	in, eventsSet, err := decodeWebhookInput(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	var t WebhookTarget
	in.apply(&t, eventsSet)
	if !validWebhookURL(t.URL) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "url http(s) obrigatória"})
		return
	}
	err = sess.updateWebhooks(r.Context(), func(list []WebhookTarget) ([]WebhookTarget, error) {
		// o 1º webhook da sessão vira o "default" (o da rota antiga /webhook)
		t.ID = webhookDefaultID
		for _, h := range list {
			if h.ID == webhookDefaultID {
				t.ID = whNewID()
			}
		}
		return append(list, t), nil
	})
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusCreated, webhookViewWithState(sess.id, t))
}

// PUT /api/sessions/{sid}/webhooks/{wid}
func (s *server) handleUpdateWebhook(w http.ResponseWriter, r *http.Request) {
	sess := s.sessionByID(w, r.PathValue("sid"))
	if sess == nil {
		return
	}
	wid := r.PathValue("wid")
	in, eventsSet, err := decodeWebhookInput(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	if in.URL != nil && !validWebhookURL(strings.TrimSpace(*in.URL)) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "url http(s) obrigatória"})
		return
	}
	var out WebhookTarget
	err = sess.updateWebhooks(r.Context(), func(list []WebhookTarget) ([]WebhookTarget, error) {
		for i := range list {
			if list[i].ID == wid {
				in.apply(&list[i], eventsSet)
				out = list[i]
				return list, nil
			}
		}
		return nil, errWebhookNotFound
	})
	if errors.Is(err, errWebhookNotFound) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	} else if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, webhookViewWithState(sess.id, out))
}

// DELETE /api/sessions/{sid}/webhooks/{wid}
func (s *server) handleDeleteWebhookByID(w http.ResponseWriter, r *http.Request) {
	sess := s.sessionByID(w, r.PathValue("sid"))
	if sess == nil {
		return
	}
	wid := r.PathValue("wid")
	err := sess.updateWebhooks(r.Context(), func(list []WebhookTarget) ([]WebhookTarget, error) {
		for i := range list {
			if list[i].ID == wid {
				return append(list[:i:i], list[i+1:]...), nil
			}
		}
		return nil, errWebhookNotFound
	})
	if errors.Is(err, errWebhookNotFound) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	} else if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	whDeliver.forget(sess.id, wid)
	w.WriteHeader(http.StatusNoContent)
}

// POST /api/sessions/{sid}/webhooks/{wid}/test — envia um evento "test" na hora
// (sem retry/DLQ) e devolve o status HTTP que o endpoint respondeu.
func (s *server) handleTestWebhook(w http.ResponseWriter, r *http.Request) {
	sess := s.sessionByID(w, r.PathValue("sid"))
	if sess == nil {
		return
	}
	t, ok := sess.webhookByID(r.PathValue("wid"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": errWebhookNotFound.Error()})
		return
	}
	body, _ := json.Marshal(map[string]any{
		"session": sess.id, "event": "test", "timestamp": time.Now().UnixMilli(),
		"data": map[string]any{"webhookId": t.ID, "message": "teste de webhook do AstraCalls"},
	})
	code, err := whDeliver.post(t.URL, body, t.Secret)
	res := map[string]any{"ok": err == nil && code >= 200 && code < 300, "status": code}
	if err != nil {
		res["error"] = err.Error()
	}
	writeJSON(w, http.StatusOK, res)
}

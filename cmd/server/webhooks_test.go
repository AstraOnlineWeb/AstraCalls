package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type hookSink struct {
	mu     sync.Mutex
	events []string
	sigs   []string
}

func (h *hookSink) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var d struct {
			Event string `json:"event"`
		}
		_ = json.Unmarshal(b, &d)
		h.mu.Lock()
		h.events = append(h.events, d.Event)
		h.sigs = append(h.sigs, r.Header.Get("X-Webhook-Signature"))
		h.mu.Unlock()
	}
}

func (h *hookSink) got() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.events...)
}

func TestDispatchToMultipleWebhooks(t *testing.T) {
	a, b := &hookSink{}, &hookSink{}
	sa, sb := httptest.NewServer(a.handler()), httptest.NewServer(b.handler())
	defer sa.Close()
	defer sb.Close()

	s := &Session{id: "multi"}
	err := s.updateWebhooks(context.Background(), func([]WebhookTarget) ([]WebhookTarget, error) {
		return []WebhookTarget{
			{ID: webhookDefaultID, URL: sa.URL, Secret: "k"},
			{ID: "h2", URL: sb.URL, Events: []string{"message"}, IgnoreGroups: true},
		}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	s.dispatchWebhook("message", map[string]any{"chat": "5511@s.whatsapp.net"})
	s.dispatchWebhook("message", map[string]any{"chat": "1203@g.us", "isGroup": true})
	s.dispatchWebhook("receipt", map[string]any{"chat": "5511@s.whatsapp.net"})

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && (len(a.got()) < 3 || len(b.got()) < 1) {
		time.Sleep(20 * time.Millisecond)
	}
	time.Sleep(100 * time.Millisecond)
	if n := len(a.got()); n != 3 {
		t.Fatalf("default deveria receber 3 eventos, got %d %v", n, a.got())
	}
	if g := b.got(); len(g) != 1 || g[0] != "message" {
		t.Fatalf("h2 (só message, sem grupo) deveria receber 1 message, got %v", g)
	}
	a.mu.Lock()
	if !strings.HasPrefix(a.sigs[0], "sha256=") {
		t.Fatalf("default com secret deveria assinar, got %q", a.sigs[0])
	}
	a.mu.Unlock()
	b.mu.Lock()
	if b.sigs[0] != "" {
		t.Fatalf("h2 sem secret não deveria assinar")
	}
	b.mu.Unlock()
}

func TestWebhookTargetWants(t *testing.T) {
	h := WebhookTarget{Events: []string{"message"}, IgnoreGroups: true}
	if !h.wants("message", map[string]any{"chat": "1@s.whatsapp.net"}) {
		t.Fatal("message 1:1 deveria passar")
	}
	if h.wants("message", map[string]any{"chat": "1@g.us"}) {
		t.Fatal("grupo deveria ser ignorado")
	}
	if h.wants("receipt", map[string]any{"chat": "1@s.whatsapp.net"}) {
		t.Fatal("receipt fora do filtro")
	}
	if !(WebhookTarget{}).wants("anything", nil) {
		t.Fatal("sem filtro = tudo")
	}
}

func TestLoadWebhooksLegacy(t *testing.T) {
	s := &Session{id: "x"}
	s.loadWebhooks(sessionRow{Webhook: "https://a", WebhookSecret: "k", WebhookEvents: "message,receipt", WebhookIgnoreGroups: true})
	h, ok := s.webhookByID(webhookDefaultID)
	if !ok || h.URL != "https://a" || h.Secret != "k" || len(h.Events) != 2 || !h.IgnoreGroups {
		t.Fatalf("legado deveria virar o default, got %+v", h)
	}
	s.loadWebhooks(sessionRow{Webhook: "https://a", Webhooks: `[{"id":"default","url":"https://b"},{"id":"z","url":"https://c"}]`})
	if hs := s.getWebhooks(); len(hs) != 2 || hs[0].URL != "https://b" {
		t.Fatalf("lista salva tem prioridade sobre o legado, got %+v", hs)
	}
}

func TestUpdateWebhooksLimit(t *testing.T) {
	s := &Session{id: "lim"}
	err := s.updateWebhooks(context.Background(), func([]WebhookTarget) ([]WebhookTarget, error) {
		return make([]WebhookTarget, webhookMax+1), nil
	})
	if err != errWebhookLimit {
		t.Fatalf("esperava errWebhookLimit, got %v", err)
	}
}

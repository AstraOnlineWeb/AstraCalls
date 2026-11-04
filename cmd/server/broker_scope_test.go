package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// drain lê os eventos já enfileirados num assinante, sem bloquear.
func drain(s *subscriber) []map[string]any {
	var out []map[string]any
	for {
		select {
		case data := <-s.ch:
			var m map[string]any
			_ = json.Unmarshal(data, &m)
			out = append(out, m)
		default:
			return out
		}
	}
}

func hasType(evs []map[string]any, typ string) bool {
	for _, e := range evs {
		if e["type"] == typ {
			return true
		}
	}
	return false
}

// A chamada recebida de uma sessão da conta 5 só pode tocar no painel admin e no
// widget da conta 5 — nunca no widget da conta 9 (o bug relatado).
func TestEmitIncomingEscopadoPorConta(t *testing.T) {
	b := NewBroker()
	b.AccountForSession = func(sessionID string) int {
		if sessionID == "sess-da-conta-5" {
			return 5
		}
		return 0
	}

	admin := b.subscribe("painel", 0) // sem escopo: recebe tudo
	widget5 := b.subscribe("w5", 5)   // widget da conta 5
	widget9 := b.subscribe("w9", 9)   // widget da conta 9

	b.emitIncoming("sess-da-conta-5", "call-1", "5511999999999@lid", "5511999999999", "", false)

	if !hasType(drain(admin), "incoming") {
		t.Error("painel admin deveria receber a chamada recebida")
	}
	if !hasType(drain(widget5), "incoming") {
		t.Error("widget da conta 5 (dona da sessão) deveria receber")
	}
	if hasType(drain(widget9), "incoming") {
		t.Error("widget da conta 9 NÃO deveria receber (bug de vazamento entre empresas)")
	}
}

// Chamada de uma sessão SEM Chatwoot (conta 0) não deve tocar em widget nenhum —
// só aparece no painel admin.
func TestEmitIncomingSessaoSemChatwoot(t *testing.T) {
	b := NewBroker()
	b.AccountForSession = func(string) int { return 0 } // sessão sem Chatwoot

	admin := b.subscribe("painel", 0)
	widget := b.subscribe("w7", 7)

	b.emitIncoming("sess-sem-cw", "call-x", "peer", "", "", false)

	if !hasType(drain(admin), "incoming") {
		t.Error("painel admin deveria receber")
	}
	if hasType(drain(widget), "incoming") {
		t.Error("widget de conta específica não deveria receber chamada de sessão sem Chatwoot")
	}
}

// Sem AccountForSession injetado, o comportamento degrada com segurança: só quem
// não tem escopo recebe (não volta ao broadcast global para os widgets).
func TestEmitIncomingSemResolvedor(t *testing.T) {
	b := NewBroker() // AccountForSession == nil
	admin := b.subscribe("painel", 0)
	widget := b.subscribe("w1", 1)

	b.emitIncoming("qualquer", "c1", "p", "", "", false)

	if !hasType(drain(admin), "incoming") {
		t.Error("admin deveria receber mesmo sem resolvedor")
	}
	if hasType(drain(widget), "incoming") {
		t.Error("widget não deveria receber sem resolvedor (conta não confere com 0)")
	}
}

// Duas instalações de Chatwoot com a MESMA conta 1: a chamada da sessão ligada ao
// chatwoot-a só toca no widget do chatwoot-a (e num widget de origem desconhecida,
// que segue escopado só pela conta, como antes).
func TestEmitIncomingEscopadoPorHostDoChatwoot(t *testing.T) {
	b := NewBroker()
	b.AccountForSession = func(string) int { return 1 }
	b.HostForSession = func(string) string { return "chatwoot-a.exemplo" }

	widgetA := b.subscribeScoped("wa", 1, "chatwoot-a.exemplo")
	widgetB := b.subscribeScoped("wb", 1, "chatwoot-b.exemplo")
	widgetSemHost := b.subscribeScoped("wx", 1, "")

	b.emitIncoming("sess-a", "call-1", "5511999999999@lid", "5511999999999", "", false)

	if !hasType(drain(widgetA), "incoming") {
		t.Error("widget do mesmo Chatwoot deveria receber")
	}
	if hasType(drain(widgetB), "incoming") {
		t.Error("widget da conta 1 de OUTRO Chatwoot não deveria receber")
	}
	if !hasType(drain(widgetSemHost), "incoming") {
		t.Error("widget sem host identificado segue escopado só pela conta")
	}
}

func TestChatwootHost(t *testing.T) {
	cases := map[string]string{
		"https://Chat.Exemplo.com.br":       "chat.exemplo.com.br",
		"https://chat.exemplo.com.br/app/x": "chat.exemplo.com.br",
		"http://10.0.0.5:3000":              "10.0.0.5:3000",
		"":                                  "",
		"null":                              "",
	}
	for in, want := range cases {
		if got := chatwootHost(in); got != want {
			t.Errorf("chatwootHost(%q) = %q, quero %q", in, got, want)
		}
	}
}

// Caixa (inbox): widget restrito à caixa 61 não toca com chamada da caixa 62 da
// mesma conta; call-list/call-status/call-ended/QR/session-list também respeitam.
func TestEventosEscopadosPorCaixaEConta(t *testing.T) {
	b := NewBroker()
	acct := map[string]int{"s61": 7, "s62": 7, "s9": 9}
	inbox := map[string]int{"s61": 61, "s62": 62, "s9": 90}
	b.AccountForSession = func(sid string) int { return acct[sid] }
	b.InboxForSession = func(sid string) int { return inbox[sid] }

	admin := b.subscribe("painel", 0)
	cx61 := b.subscribeInboxes("w61", 7, "", map[int]bool{61: true})
	conta7 := b.subscribe("w7", 7)
	conta9 := b.subscribe("w9", 9)

	b.emitIncoming("s62", "c62", "", "5511", "", false)
	if hasType(drain(cx61), "incoming") {
		t.Error("widget da caixa 61 não deveria tocar com chamada da caixa 62")
	}
	if !hasType(drain(conta7), "incoming") || !hasType(drain(admin), "incoming") {
		t.Error("conta 7 sem filtro de caixa e painel deveriam receber")
	}
	drain(conta9)

	b.upsertCall(CallRecord{SessionID: "s9", CallID: "c9", Status: StatusRinging})
	b.endCall("c9", "x")
	for _, e := range drain(conta7) {
		if e["type"] == "call-status" || e["type"] == "call-ended" {
			t.Errorf("conta 7 recebeu %v da conta 9", e["type"])
		}
		if e["type"] == "call-list" {
			if calls, _ := e["calls"].([]any); len(calls) != 0 {
				t.Errorf("call-list da conta 7 vazou chamadas: %v", calls)
			}
		}
	}
	if !hasType(drain(conta9), "call-ended") {
		t.Error("conta 9 deveria receber o call-ended da própria chamada")
	}
	drain(admin)
	drain(cx61)

	b.emitSessionQR("s9", "qr")
	b.emitAuthState("s9", AuthSnapshot{QR: "qr"})
	if evs := drain(conta9); len(evs) != 0 {
		t.Errorf("QR/pareamento só no painel admin, widget recebeu %v", evs)
	}
	if !hasType(drain(admin), "session-qr") {
		t.Error("painel deveria receber o QR")
	}

	b.emitSessionList([]SessionInfo{{ID: "s61"}, {ID: "s62"}, {ID: "s9"}})
	for _, e := range drain(cx61) {
		if ss, _ := e["sessions"].([]any); len(ss) != 1 {
			t.Errorf("caixa 61 deveria ver só 1 sessão, viu %v", ss)
		}
	}
	for _, e := range drain(admin) {
		if ss, _ := e["sessions"].([]any); len(ss) != 3 {
			t.Errorf("painel deveria ver as 3 sessões, viu %v", ss)
		}
	}
}

func TestEventInboxesQueryEToken(t *testing.T) {
	r := httptest.NewRequest("GET", "/api/events?accountId=7&inboxIds=61,62", nil)
	if got := eventInboxes(r); len(got) != 2 || !got[61] || !got[62] {
		t.Errorf("inboxIds=61,62 → %v", got)
	}
	r = httptest.NewRequest("GET", "/api/events?accountId=7&inboxIds=0", nil)
	if got := eventInboxes(r); got == nil || len(got) != 0 {
		t.Errorf("inboxIds=0 deveria ser filtro vazio (não recebe nada), veio %v", got)
	}
	r = httptest.NewRequest("GET", "/api/events?accountId=7", nil)
	if got := eventInboxes(r); got != nil {
		t.Errorf("sem inboxIds = sem filtro, veio %v", got)
	}
	tok := signWidgetToken(widgetTokenClaims{AccountID: 7, InboxID: 61, Exp: time.Now().Add(time.Hour).Unix()})
	r = httptest.NewRequest("GET", "/api/events?accountId=7&inboxIds=62&apiKey="+tok, nil)
	if got := eventInboxes(r); len(got) != 1 || !got[61] {
		t.Errorf("token com caixa 61 deve forçar a caixa 61, veio %v", got)
	}
}

func TestWidgetTokenSoOperaSessaoDaPropriaConta(t *testing.T) {
	t.Setenv("WACALLS_API_KEY", "mestra")
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	scope := func(sid string) (int, int) { return map[string]int{"s7": 7, "s9": 9}[sid], 61 }
	h := withAuth(ok, "mestra", "estatica", scope)
	tok := signWidgetToken(widgetTokenClaims{AccountID: 7, Exp: time.Now().Add(time.Hour).Unix()})
	for path, want := range map[string]int{
		"/api/sessions/s7/calls":                               http.StatusNoContent,
		"/api/sessions/s9/calls":                               http.StatusForbidden,
		"/api/chatwoot/resolve?account_id=7&conversation_id=1": http.StatusNoContent,
		"/api/chatwoot/resolve?account_id=9&conversation_id=1": http.StatusForbidden,
	} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", path, nil)
		req.Header.Set("X-API-Key", tok)
		h.ServeHTTP(rec, req)
		if rec.Code != want {
			t.Errorf("%s: %d, queria %d", path, rec.Code, want)
		}
	}
	// chave estática sem conta no SSE não vira "painel admin"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/events?apiKey=estatica", nil))
	if rec.Code != http.StatusForbidden {
		t.Errorf("SSE com chave de widget sem conta: %d, queria 403", rec.Code)
	}
}

package main

import (
	"net/http/httptest"
	"testing"
)

// Dois Chatwoots DIFERENTES (A e B) com a MESMA conta 1 no mesmo AstraCalls,
// mais uma sessão da conta 6 num terceiro Chatwoot.
func brokerDuasInstancias() *Broker {
	b := NewBroker()
	b.AccountForSession = func(sid string) int {
		switch sid {
		case "sess-a", "sess-b":
			return 1
		case "sess-conta6":
			return 6
		}
		return 0
	}
	b.HostForSession = func(sid string) string {
		switch sid {
		case "sess-a":
			return "chatwoot-a.exemplo"
		case "sess-b":
			return "chatwoot-b.exemplo"
		case "sess-conta6":
			return "chatwoot-c.exemplo"
		}
		return ""
	}
	return b
}

// call-status e call-ended saíam por broadcast GLOBAL: chegavam até em outra conta.
func TestCallStatusEEndedEscopadosPorSessao(t *testing.T) {
	b := brokerDuasInstancias()
	a := b.subscribeScoped("astrachat_acc1", 1, "chatwoot-a.exemplo")
	conta6 := b.subscribeScoped("astrachat_acc6", 6, "chatwoot-c.exemplo")

	b.upsertCall(CallRecord{SessionID: "sess-b", CallID: "c1", Direction: "inbound", Status: StatusRinging})
	b.endCall("c1", "user_ended")

	for nome, s := range map[string]*subscriber{"Chatwoot A": a, "conta 6": conta6} {
		evs := drain(s)
		if hasType(evs, "call-status") {
			t.Errorf("%s NÃO deveria receber call-status de sessão do Chatwoot B", nome)
		}
		if hasType(evs, "call-ended") {
			t.Errorf("%s NÃO deveria receber call-ended de sessão do Chatwoot B", nome)
		}
	}
}

// O retrato call-list é a lista de TODAS as chamadas vivas; cada assinante só pode
// ver as das sessões do seu escopo (o listener do Chatwoot toca a partir dele).
func TestCallListFiltradoPorAssinante(t *testing.T) {
	b := brokerDuasInstancias()
	admin := b.subscribe("painel", 0)
	a := b.subscribeScoped("astrachat_acc1", 1, "chatwoot-a.exemplo")

	b.upsertCall(CallRecord{SessionID: "sess-a", CallID: "da-a", Direction: "inbound", Status: StatusRinging})
	b.upsertCall(CallRecord{SessionID: "sess-b", CallID: "da-b", Direction: "inbound", Status: StatusRinging})
	b.upsertCall(CallRecord{SessionID: "sess-conta6", CallID: "da-conta6", Direction: "inbound", Status: StatusRinging})

	ids := func(evs []map[string]any) map[string]bool {
		out := map[string]bool{}
		var ultimo map[string]any
		for _, e := range evs {
			if e["type"] == "call-list" {
				ultimo = e
			}
		}
		if ultimo == nil {
			return out
		}
		for _, c := range ultimo["calls"].([]any) {
			out[c.(map[string]any)["callId"].(string)] = true
		}
		return out
	}

	got := ids(drain(a))
	if !got["da-a"] || got["da-b"] || got["da-conta6"] || len(got) != 1 {
		t.Errorf("call-list do Chatwoot A deveria ter só a chamada dele, veio %v", got)
	}
	if len(ids(drain(admin))) != 3 {
		t.Error("painel admin deveria ver as 3 chamadas no call-list")
	}
}

// O listener do Chatwoot (lado servidor) não manda Origin: ele se identifica pelo
// chatwootUrl. Só vale host de Chatwoot conhecido — alias/PWA ficam sem host.
func TestHostDoAssinanteSSE(t *testing.T) {
	scopeHost := func(raw string) string {
		if h := chatwootHost(raw); h == "chatwoot-a.exemplo" || h == "chatwoot-b.exemplo" {
			return h
		}
		return ""
	}
	casos := []struct {
		nome, url, origin, want string
	}{
		{"listener com chatwootUrl", "/api/events?accountId=1&clientId=astrachat_acc1&chatwootUrl=https%3A%2F%2Fchatwoot-b.exemplo", "", "chatwoot-b.exemplo"},
		{"chatwootUrl vence origin", "/api/events?accountId=1&chatwootUrl=https%3A%2F%2Fchatwoot-b.exemplo", "https://chatwoot-a.exemplo", "chatwoot-b.exemplo"},
		{"navegador pelo origin", "/api/events?accountId=1", "https://chatwoot-a.exemplo", "chatwoot-a.exemplo"},
		{"chatwootUrl desconhecido cai no origin", "/api/events?accountId=1&chatwootUrl=https%3A%2F%2Falias.chatwoot-a.exemplo", "https://chatwoot-a.exemplo", "chatwoot-a.exemplo"},
		{"origin desconhecido (alias/PWA)", "/api/events?accountId=1", "https://app.chatwoot-a.exemplo", ""},
		{"listener antigo sem nada", "/api/events?accountId=1&clientId=astrachat_acc1", "", ""},
	}
	for _, c := range casos {
		r := httptest.NewRequest("GET", c.url, nil)
		if c.origin != "" {
			r.Header.Set("Origin", c.origin)
		}
		if got := sseHost(r, scopeHost); got != c.want {
			t.Errorf("%s: host = %q, quero %q", c.nome, got, c.want)
		}
	}
}

// Ponta a ponta com a ligação real do server.go: o navegador do Chatwoot A entra pelo
// Origin e o listener do Chatwoot B pelo chatwootUrl — a chamada de B só chega em B.
func TestSessionManagerSeparaChatwootsDaMesmaConta(t *testing.T) {
	broker := NewBroker()
	mgr := &SessionManager{broker: broker, sessions: map[string]*Session{}}
	broker.AccountForSession = mgr.accountIDForSession
	broker.HostForSession = mgr.hostForSession
	for id, url := range map[string]string{"sess-a": "https://chatwoot-a.exemplo", "sess-b": "https://chatwoot-b.exemplo/"} {
		s := &Session{id: id, mgr: mgr, reg: newCallRegistry()}
		s.setChatwoot(ChatwootConfig{URL: url, AccountID: 1, AccountToken: "t", InboxID: 1})
		mgr.register(s)
	}

	rA := httptest.NewRequest("GET", "/api/events?accountId=1", nil)
	rA.Header.Set("Origin", "https://chatwoot-a.exemplo")
	rAListener := httptest.NewRequest("GET", "/api/events?accountId=1&clientId=astrachat_acc1&chatwootUrl=https%3A%2F%2Fchatwoot-a.exemplo", nil)
	rB := httptest.NewRequest("GET", "/api/events?accountId=1&clientId=astrachat_acc1&chatwootUrl=https%3A%2F%2Fchatwoot-b.exemplo", nil)
	a := broker.subscribeScoped("navegador-a", 1, sseHost(rA, mgr.chatwootScopeHost))
	aListener := broker.subscribeScoped("astrachat_acc1", 1, sseHost(rAListener, mgr.chatwootScopeHost))
	b := broker.subscribeScoped("astrachat_acc1", 1, sseHost(rB, mgr.chatwootScopeHost))

	broker.emitIncoming("sess-b", "call-1", "5561999999999", "5561999999999", "", false)

	if hasType(drain(a), "incoming") {
		t.Error("navegador do Chatwoot A NÃO deveria receber a chamada do Chatwoot B")
	}
	if hasType(drain(aListener), "incoming") {
		t.Error("listener do Chatwoot A NÃO deveria receber a chamada do Chatwoot B")
	}
	if !hasType(drain(b), "incoming") {
		t.Error("listener do Chatwoot B deveria receber a própria chamada")
	}
}

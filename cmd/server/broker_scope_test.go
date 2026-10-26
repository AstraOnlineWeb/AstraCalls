package main

import (
	"encoding/json"
	"testing"
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

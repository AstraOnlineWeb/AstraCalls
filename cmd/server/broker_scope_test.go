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
	widget5 := b.subscribe("w5", 5)    // widget da conta 5
	widget9 := b.subscribe("w9", 9)    // widget da conta 9

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

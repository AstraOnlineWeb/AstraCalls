package main

import "testing"

// Baseado no teste de @diegotiemann (PR #11): garante que contatos de grupo
// legado (@g.us) são distinguidos dos JIDs 1:1.
func TestIsGroupChatID(t *testing.T) {
	if !isGroupChatID("5511999999999-1531238647@g.us") {
		t.Fatal("esperava JID de grupo")
	}
	if isGroupChatID("5511999999999@s.whatsapp.net") {
		t.Fatal("esperava JID individual")
	}
	if isGroupChatID("123@newsletter") {
		t.Fatal("canal não é grupo")
	}
}

func TestChatwootSenderName(t *testing.T) {
	cases := []struct {
		name string
		body map[string]any
		want string
	}{
		{"agente user", map[string]any{"sender": map[string]any{"type": "user", "name": "João"}}, "João"},
		{"fallback available_name", map[string]any{"sender": map[string]any{"type": "user", "available_name": "Maria"}}, "Maria"},
		{"sem sender", map[string]any{}, ""},
		{"sem nome", map[string]any{"sender": map[string]any{"type": "user"}}, ""},
		{"tipo contato ignorado", map[string]any{"sender": map[string]any{"type": "contact", "name": "Cliente"}}, ""},
		{"tipo bot ignorado", map[string]any{"sender": map[string]any{"type": "agent_bot", "name": "Bot"}}, ""},
	}
	for _, c := range cases {
		if got := chatwootSenderName(c.body); got != c.want {
			t.Errorf("%s: chatwootSenderName = %q, quer %q", c.name, got, c.want)
		}
	}
}

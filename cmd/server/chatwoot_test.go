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

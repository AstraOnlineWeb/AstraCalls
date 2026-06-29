package main

import "testing"

func TestIsGroupChatID(t *testing.T) {
	if !isGroupChatID("5511999999999-1531238647@g.us") {
		t.Fatal("expected group JID")
	}
	if isGroupChatID("5511999999999@s.whatsapp.net") {
		t.Fatal("expected individual JID")
	}
}

func TestEnsureContactSkipsWrongGroup(t *testing.T) {
	groupLegacy := map[string]any{"identifier": "5511999999999-1531238647@g.us"}
	chat1to1 := "5511999999999@s.whatsapp.net"
	ident := asStr(groupLegacy["identifier"])
	if !(isGroupChatID(ident) && ident != chat1to1) {
		t.Fatal("1:1 must skip legacy group on search")
	}
	groupChat := "5511999999999-1531238647@g.us"
	if isGroupChatID(ident) && ident != groupChat {
		t.Fatal("group message must not skip matching group contact")
	}
}

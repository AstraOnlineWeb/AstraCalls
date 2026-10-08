package main

import (
	"testing"

	waBinary "go.mau.fi/whatsmeow/binary"
)

func TestCallIDFromNode(t *testing.T) {
	node := &waBinary.Node{
		Tag: "call",
		Content: []waBinary.Node{{
			Tag:   "offer",
			Attrs: waBinary.Attrs{"call-id": "ABC123"},
		}},
	}
	if got := callIDFromNode(node); got != "ABC123" {
		t.Fatalf("expected ABC123, got %q", got)
	}

	empty := &waBinary.Node{Tag: "call"}
	if got := callIDFromNode(empty); got != "" {
		t.Fatalf("node with no children must yield empty, got %q", got)
	}
}

// Convite de chamada em GRUPO (offer com group-jid / <group_info>) não é
// chamada 1:1 e não pode ser tratado como tal (preaccept/incoming/reject em
// nome do participante derrubam a entrada dele pelo celular).
func TestGroupJIDFromOffer(t *testing.T) {
	offer := func(attrs waBinary.Attrs, kids ...waBinary.Node) *waBinary.Node {
		return &waBinary.Node{Tag: "call", Content: []waBinary.Node{{Tag: "offer", Attrs: attrs, Content: kids}}}
	}
	cases := []struct {
		name string
		node *waBinary.Node
		want string
	}{
		{"1:1", offer(waBinary.Attrs{"call-id": "A"}), ""},
		{"group-jid string", offer(waBinary.Attrs{"call-id": "B", "group-jid": "120363431786799787@g.us"}), "120363431786799787@g.us"},
		{"group-jid vazio", offer(waBinary.Attrs{"call-id": "C", "group-jid": ""}), ""},
		{"só group_info", offer(waBinary.Attrs{"call-id": "D"}, waBinary.Node{Tag: "group_info"}), "(group_info)"},
		{"não é offer", &waBinary.Node{Tag: "call", Content: []waBinary.Node{{Tag: "terminate", Attrs: waBinary.Attrs{"call-id": "E", "group-jid": "x@g.us"}}}}, ""},
		{"sem filhos", &waBinary.Node{Tag: "call"}, ""},
	}
	for _, c := range cases {
		if got := groupJIDFromOffer(c.node); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
		if got := isGroupCallOffer(c.node); got != (c.want != "") {
			t.Errorf("%s: isGroupCallOffer=%v", c.name, got)
		}
	}
}

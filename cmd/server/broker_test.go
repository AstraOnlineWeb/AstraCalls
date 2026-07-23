package main

import "testing"

func ownerPtr(s string) *string { return &s }

func TestOwnerActiveCall(t *testing.T) {
	b := NewBroker()
	b.upsertCall(CallRecord{SessionID: "s1", CallID: "c1", Owner: ownerPtr("op-A"), Status: StatusConnected})
	b.upsertCall(CallRecord{SessionID: "s1", CallID: "c2", Owner: ownerPtr("op-B"), Status: StatusRinging})

	if got := b.ownerActiveCall("op-A"); got != "c1" {
		t.Fatalf("op-A should own c1, got %q", got)
	}
	if got := b.ownerActiveCall("op-C"); got != "" {
		t.Fatalf("op-C owns nothing, got %q", got)
	}
	if got := b.ownerActiveCall(""); got != "" {
		t.Fatalf("empty owner must return empty, got %q", got)
	}

	b.endCall("c1", "done")
	if got := b.ownerActiveCall("op-A"); got != "" {
		t.Fatalf("op-A's call ended, expected empty, got %q", got)
	}
}

func TestReassignOwner(t *testing.T) {
	b := NewBroker()
	b.upsertCall(CallRecord{SessionID: "s1", CallID: "c1", Owner: ownerPtr("op-A"), Status: StatusConnected})

	// libera o dono (transferência oferecida)
	if !b.reassignOwner("c1", nil) {
		t.Fatal("reassignOwner deveria achar c1")
	}
	if c, ok := b.getCall("c1"); !ok || c.Owner != nil {
		t.Fatalf("esperava owner nil após liberar, got %+v", c)
	}

	// fixa novo dono (pickup)
	b.reassignOwner("c1", ownerPtr("op-B"))
	if c, ok := b.getCall("c1"); !ok || c.Owner == nil || *c.Owner != "op-B" {
		t.Fatalf("esperava owner op-B após pickup, got %+v", c)
	}
	if got := b.ownerActiveCall("op-B"); got != "c1" {
		t.Fatalf("op-B deveria ser dono de c1, got %q", got)
	}

	// chamada inexistente
	if b.reassignOwner("nao-existe", ownerPtr("x")) {
		t.Fatal("reassignOwner de callID inexistente deveria falhar")
	}
}

package main

import (
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestAgentsRosterAndTransferName(t *testing.T) {
	b := NewBroker()
	a := b.subscribe("agent-a", 0)
	a.label = "João"
	c := b.subscribe("agent-b", 0)
	c.label = "Maria"
	b.upsertCall(CallRecord{CallID: "C1", SessionID: "s1", Status: StatusConnected, Owner: strPtr("agent-a")})

	list := b.agentsFor("s1")
	if len(list) != 2 || list[0].Label != "João" || !list[0].Busy || list[0].CallID != "C1" || list[1].Busy {
		t.Fatalf("roster: %+v", list)
	}
	if !b.isConnected("s1", "agent-b") || b.isConnected("s1", "ninguem") {
		t.Fatal("isConnected")
	}
	if !b.setLabel("agent-b", "Maria S.") || b.labelOf("agent-b") != "Maria S." {
		t.Fatal("setLabel")
	}
	drainSub(c)
	// sem fromLabel: usa o label registrado de quem transferiu; só o alvo recebe
	b.emitTransferOffer("s1", "C1", "5561", "agent-a", "", "agent-b")
	ev := next(t, c)
	if ev["type"] != "call-transfer-offer" || ev["fromName"] != "João" || ev["toName"] != "Maria S." {
		t.Fatalf("evento: %v", ev)
	}
	b.emitTransferOffer("s1", "C1", "5561", "agent-a", "Supervisor", "agent-b")
	if ev := next(t, c); ev["fromName"] != "Supervisor" {
		t.Fatalf("fromLabel: %v", ev)
	}
}

func strPtr(s string) *string { return &s }

func drainSub(s *subscriber) {
	for {
		select {
		case <-s.ch:
		default:
			return
		}
	}
}

func next(t *testing.T, s *subscriber) map[string]any {
	t.Helper()
	for {
		select {
		case d := <-s.ch:
			var m map[string]any
			_ = json.Unmarshal(d, &m)
			if m["type"] == "call-list" || m["type"] == "call-status" || m["type"] == "agents-changed" {
				continue
			}
			return m
		case <-time.After(time.Second):
			t.Fatal("sem evento")
		}
	}
}

func TestMOHCache(t *testing.T) {
	calls := 0
	old := mohLoader
	defer func() { mohLoader = old }()
	mohCacheMu.Lock()
	mohCache = map[string]mohEntry{}
	mohCacheMu.Unlock()
	mohLoader = func(url string) ([]float32, error) {
		calls++
		if url == "ruim" {
			return nil, errors.New("x")
		}
		return []float32{0.1, 0.2}, nil
	}
	if p, err := mohPCM("boa"); err != nil || len(p) != 2 {
		t.Fatal(err)
	}
	_, _ = mohPCM("boa")
	if calls != 1 {
		t.Fatalf("cache não usado: %d", calls)
	}
	if _, err := mohPCM("ruim"); err == nil {
		t.Fatal("esperava erro")
	}
}

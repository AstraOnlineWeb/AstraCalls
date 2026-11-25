package main

import (
	"log/slog"
	"testing"
	"time"
)

func newQueueTestServer() (*server, *Broker) {
	b := NewBroker()
	s := &server{broker: b, log: slog.Default()}
	s.queue = newTransferQueue(s)
	return s, b
}

func targetOf(q *transferQueue, id string) (string, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	_, e := q.find(id)
	if e == nil {
		return "", false
	}
	return e.target, true
}

func expire(q *transferQueue, id string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	_, e := q.find(id)
	e.offeredAt = time.Now().Add(-time.Minute)
}

func TestTransferQueueRingNext(t *testing.T) {
	s, b := newQueueTestServer()
	b.subscribe("A", 0).label = "Ana"
	b.subscribe("B", 0).label = "Bia"
	b.subscribe("C", 0).label = "Caio"
	b.upsertCall(CallRecord{CallID: "Q1", SessionID: "s1", Status: StatusConnected})

	if mode := s.offerTransfer("s1", "Q1", "5561", "A", "", "", "", 5); mode != "queue" {
		t.Fatalf("mode=%s", mode)
	}
	s.queue.dispatch()
	first, _ := targetOf(s.queue, "Q1")
	if first != "B" && first != "C" {
		t.Fatalf("oferta para %q (não pode ser quem transferiu)", first)
	}
	other := map[string]string{"B": "C", "C": "B"}[first]
	if ok, _ := s.queue.claim("Q1", other); ok {
		t.Fatal("quem não tem a oferta não pode assumir")
	}
	if !s.queue.decline("Q1", first) {
		t.Fatal("decline")
	}
	s.queue.dispatch()
	if tg, _ := targetOf(s.queue, "Q1"); tg != other {
		t.Fatalf("depois de recusar devia ir para %s, foi %q", other, tg)
	}
	// expira: volta para o primeiro (nova volta)
	expire(s.queue, "Q1")
	s.queue.dispatch()
	if tg, _ := targetOf(s.queue, "Q1"); tg != first {
		t.Fatalf("após expirar devia voltar para %s, foi %q", first, tg)
	}
	if ok, _ := s.queue.claim("Q1", first); !ok {
		t.Fatal("o alvo deve assumir")
	}
	if _, in := targetOf(s.queue, "Q1"); in {
		t.Fatal("devia sair da fila")
	}
}

func TestTransferQueueFIFOAndLock(t *testing.T) {
	s, b := newQueueTestServer()
	b.subscribe("A", 0)
	b.subscribe("B", 0)
	b.upsertCall(CallRecord{CallID: "X1", SessionID: "s1", Status: StatusConnected})
	b.upsertCall(CallRecord{CallID: "X2", SessionID: "s1", Status: StatusConnected})
	s.offerTransfer("s1", "X1", "", "A", "", "", "", 0)
	time.Sleep(5 * time.Millisecond)
	s.offerTransfer("s1", "X2", "", "A", "", "", "", 0)
	s.queue.dispatch()
	t1, _ := targetOf(s.queue, "X1")
	t2, _ := targetOf(s.queue, "X2")
	if t1 != "B" || t2 != "" {
		t.Fatalf("FIFO: X1→%q X2→%q (só B livre; X1 primeiro)", t1, t2)
	}
	// direcionada: só o alvo assume
	b.upsertCall(CallRecord{CallID: "D1", SessionID: "s1", Status: StatusConnected})
	s.offerTransfer("s1", "D1", "", "A", "", "B", "", 0)
	if ok, _ := s.queue.claim("D1", "A"); ok {
		t.Fatal("direcionada para B não pode ir para A")
	}
	// trava de dono
	if ok, _ := b.claimOwner("D1", "B"); !ok {
		t.Fatal("B assume")
	}
	if ok, _ := b.claimOwner("D1", "A"); ok {
		t.Fatal("segundo dono não pode")
	}
	// modo all: qualquer um
	b.upsertCall(CallRecord{CallID: "L1", SessionID: "s1", Status: StatusConnected})
	s.offerTransfer("s1", "L1", "", "A", "", "", "all", 0)
	if ok, _ := s.queue.claim("L1", "A"); !ok {
		t.Fatal("modo all livre")
	}
}

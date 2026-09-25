package main

import (
	"log/slog"
	"net/http"
	"testing"
)

func newTestDeliverer() *webhookDeliverer {
	return &webhookDeliverer{
		circuits: map[string]*whCircuit{},
		dlq:      map[string][]whDLQItem{},
		client:   &http.Client{},
		log:      slog.Default(),
	}
}

func TestWebhookCircuitOpensAndDisables(t *testing.T) {
	d := newTestDeliverer()
	sid := "s1"
	for i := 0; i < whOpenThreshold-1; i++ {
		d.onFailure(sid, "http://x", "e", []byte("{}"), "boom", "")
	}
	if st := d.status(sid)["state"]; st != "closed" {
		t.Fatalf("antes do limiar deveria estar closed, got %v", st)
	}
	d.onFailure(sid, "http://x", "e", []byte("{}"), "boom", "") // atinge whOpenThreshold
	if st := d.status(sid)["state"]; st != "open" {
		t.Fatalf("no limiar deveria abrir, got %v", st)
	}
	// sucesso zera o circuito
	d.onSuccess(sid)
	if st := d.status(sid)["state"]; st != "closed" {
		t.Fatalf("após sucesso deveria fechar, got %v", st)
	}
	// hard disable
	for i := 0; i < whHardDisable; i++ {
		d.onFailure(sid, "http://x", "e", []byte("{}"), "boom", "")
	}
	if st := d.status(sid)["state"]; st != "disabled" {
		t.Fatalf("acima do hard disable deveria desabilitar, got %v", st)
	}
	d.reenable(sid)
	if st := d.status(sid)["state"]; st != "closed" {
		t.Fatalf("após reenable deveria fechar, got %v", st)
	}
}

func TestWebhookDLQCapacityAndReplayNotFound(t *testing.T) {
	d := newTestDeliverer()
	sid := "s2"
	for i := 0; i < whDLQCapacity+10; i++ {
		d.pushDLQ(sid, "http://x", "e", []byte("{}"), "boom", 3, "")
	}
	if n := len(d.dlqList(sid)); n != whDLQCapacity {
		t.Fatalf("DLQ deveria respeitar a capacidade %d, got %d", whDLQCapacity, n)
	}
	if err := d.dlqReplay(sid, "inexistente"); err != errDLQNotFound {
		t.Fatalf("replay de id inexistente deveria dar errDLQNotFound, got %v", err)
	}
}

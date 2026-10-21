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
	tg := whTarget{session: sid, hook: "default", url: "http://x"}
	other := whTarget{session: sid, hook: "h2", url: "http://y"}
	for i := 0; i < whOpenThreshold-1; i++ {
		d.onFailure(tg, "e", []byte("{}"), "boom")
	}
	if st := d.status(sid, []string{"default"})["state"]; st != "closed" {
		t.Fatalf("antes do limiar deveria estar closed, got %v", st)
	}
	d.onFailure(tg, "e", []byte("{}"), "boom") // atinge whOpenThreshold
	if st := d.status(sid, []string{"default"})["state"]; st != "open" {
		t.Fatalf("no limiar deveria abrir, got %v", st)
	}
	// sucesso zera o circuito
	d.onSuccess(tg)
	if st := d.status(sid, []string{"default"})["state"]; st != "closed" {
		t.Fatalf("após sucesso deveria fechar, got %v", st)
	}
	// hard disable
	for i := 0; i < whHardDisable; i++ {
		d.onFailure(tg, "e", []byte("{}"), "boom")
	}
	if st := d.status(sid, []string{"default"})["state"]; st != "disabled" {
		t.Fatalf("acima do hard disable deveria desabilitar, got %v", st)
	}
	// circuito é por webhook: o outro da mesma sessão segue fechado
	if st := d.hookState(sid, other.hook)["state"]; st != "closed" {
		t.Fatalf("outro webhook deveria seguir closed, got %v", st)
	}
	d.reenable(sid, "")
	if st := d.status(sid, []string{"default"})["state"]; st != "closed" {
		t.Fatalf("após reenable deveria fechar, got %v", st)
	}
}

func TestWebhookDLQCapacityAndReplayNotFound(t *testing.T) {
	d := newTestDeliverer()
	sid := "s2"
	for i := 0; i < whDLQCapacity+10; i++ {
		d.pushDLQ(whTarget{session: sid, hook: "default", url: "http://x"}, "e", []byte("{}"), "boom", 3)
	}
	if n := len(d.dlqList(sid)); n != whDLQCapacity {
		t.Fatalf("DLQ deveria respeitar a capacidade %d, got %d", whDLQCapacity, n)
	}
	if err := d.dlqReplay(sid, "inexistente"); err != errDLQNotFound {
		t.Fatalf("replay de id inexistente deveria dar errDLQNotFound, got %v", err)
	}
}

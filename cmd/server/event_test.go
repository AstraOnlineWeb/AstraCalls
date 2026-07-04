package main

import (
	"strings"
	"testing"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"google.golang.org/protobuf/proto"
)

func TestEventText(t *testing.T) {
	ev := &waE2E.EventMessage{
		Name:        proto.String("Live Astra"),
		Description: proto.String("bora"),
		StartTime:   proto.Int64(1784815200), // 23/07/2026 11:00 BRT
		JoinLink:    proto.String("https://call.whatsapp.com/voice/abc"),
		Location:    &waE2E.LocationMessage{Name: proto.String("Auditório")},
	}
	got := eventText(ev)
	for _, want := range []string{"📅 *Evento: Live Astra*", "bora", "23/07/2026 11:00", "📍 Auditório", "call.whatsapp.com"} {
		if !strings.Contains(got, want) {
			t.Fatalf("eventText não contém %q. Saída:\n%s", want, got)
		}
	}
	ev.IsCanceled = proto.Bool(true)
	if !strings.Contains(eventText(ev), "CANCELADO") {
		t.Fatal("evento cancelado deveria marcar CANCELADO")
	}
	if messageType(&waE2E.Message{EventMessage: ev}) != "event" {
		t.Fatal("messageType deveria ser 'event'")
	}
}

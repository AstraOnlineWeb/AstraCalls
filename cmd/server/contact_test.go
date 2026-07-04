package main

import (
	"strings"
	"testing"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"google.golang.org/protobuf/proto"
)

const vcard1 = "BEGIN:VCARD\nVERSION:3.0\nFN:João Silva\nTEL;type=CELL;waid=5561999998888:+55 61 99999-8888\nEND:VCARD"
const vcard2 = "BEGIN:VCARD\nVERSION:3.0\nFN:Maria\nTEL;waid=5561977776666:+55 61 97777-6666\nEND:VCARD"

func TestContactText(t *testing.T) {
	cm := &waE2E.ContactMessage{DisplayName: proto.String("João Silva"), Vcard: proto.String(vcard1)}
	got := contactText(cm)
	if !strings.Contains(got, "👤 *Contato: João Silva*") || !strings.Contains(got, "+55 61 99999-8888") {
		t.Fatalf("contactText inesperado:\n%s", got)
	}
	if messageType(&waE2E.Message{ContactMessage: cm}) != "contact" {
		t.Fatal("type deveria ser contact")
	}
}

func TestContactsArrayText(t *testing.T) {
	cam := &waE2E.ContactsArrayMessage{
		Contacts: []*waE2E.ContactMessage{
			{DisplayName: proto.String("João Silva"), Vcard: proto.String(vcard1)},
			{DisplayName: proto.String("Maria"), Vcard: proto.String(vcard2)},
		},
	}
	got := contactsArrayText(cam)
	for _, want := range []string{"2 contatos", "João Silva — +55 61 99999-8888", "Maria — +55 61 97777-6666"} {
		if !strings.Contains(got, want) {
			t.Fatalf("array não contém %q:\n%s", want, got)
		}
	}
}

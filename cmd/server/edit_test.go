package main

import (
	"strings"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

// editMessage monta o ProtocolMessage MESSAGE_EDIT como o WhatsApp entrega uma
// edição: o texto novo em EditedMessage e a Key apontando pro ID original.
func editMessage(origID, newText string) *waE2E.Message {
	return &waE2E.Message{
		ProtocolMessage: &waE2E.ProtocolMessage{
			Type: waE2E.ProtocolMessage_MESSAGE_EDIT.Enum(),
			Key:  &waCommon.MessageKey{ID: proto.String(origID)},
			EditedMessage: &waE2E.Message{
				Conversation: proto.String(newText),
			},
		},
	}
}

func TestUnwrapEdit(t *testing.T) {
	m := editMessage("ORIG123", "texto corrigido")
	inner, origID, isEdit := unwrapEdit(m)
	if !isEdit {
		t.Fatal("esperava edição")
	}
	if origID != "ORIG123" {
		t.Errorf("origID = %q", origID)
	}
	if inner.GetConversation() != "texto corrigido" {
		t.Errorf("inner = %q", inner.GetConversation())
	}
	// mensagem normal não é edição
	plain := &waE2E.Message{Conversation: proto.String("oi")}
	if _, _, e := unwrapEdit(plain); e {
		t.Error("mensagem normal não deveria ser edição")
	}
}

func TestMessageTextUnwrapsEdit(t *testing.T) {
	if got := messageText(editMessage("X", "novo texto")); got != "novo texto" {
		t.Errorf("messageText da edição = %q", got)
	}
	if got := messageType(editMessage("X", "novo texto")); got != "text" {
		t.Errorf("messageType da edição = %q", got)
	}
}

func TestDeliverContentEdit(t *testing.T) {
	evt := &events.Message{
		Info: types.MessageInfo{
			ID:            "ORIG123",
			Edit:          types.EditAttributeMessageEdit,
			MessageSource: types.MessageSource{},
		},
		Message: editMessage("ORIG123", "corrigido"),
	}
	evt.Info.Timestamp = time.Unix(1700000000, 0)
	j := deliverContent(evt, "", false)
	if j.InReplyTo != "ORIG123" {
		t.Errorf("InReplyTo = %q, quer ORIG123 (ligar ao balão original)", j.InReplyTo)
	}
	if j.SourceID == "ORIG123" {
		t.Error("SourceID não pode ser igual ao original (Chatwoot deduplica e some)")
	}
	if !strings.Contains(j.Prefix, "Editada") {
		t.Errorf("faltou marcar como editada: prefix=%q", j.Prefix)
	}
	if j.Text != "corrigido" {
		t.Errorf("texto = %q", j.Text)
	}
}

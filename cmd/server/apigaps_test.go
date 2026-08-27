package main

import (
	"testing"

	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"google.golang.org/protobuf/proto"
)

func TestChatKind(t *testing.T) {
	cases := map[string]string{
		"5561999999999@s.whatsapp.net":  "individual",
		"144946606653478:54@lid":        "individual",
		"120363000000000000@g.us":       "group",
		"120363000000000000@newsletter": "channel",
		"status@broadcast":              "broadcast",
	}
	for in, want := range cases {
		if got := chatKind(in); got != want {
			t.Errorf("chatKind(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestMentionJID(t *testing.T) {
	if got := mentionJID("5561999999999"); got != "5561999999999@s.whatsapp.net" {
		t.Errorf("mentionJID(numero) = %q", got)
	}
	if got := mentionJID("5561999999999@s.whatsapp.net"); got != "5561999999999@s.whatsapp.net" {
		t.Errorf("mentionJID(jid) = %q", got)
	}
	if got := mentionJID(" +55 (61) 99999-9999 "); got != "5561999999999@s.whatsapp.net" {
		t.Errorf("mentionJID(formatado) = %q", got)
	}
}

func TestApplyContextInfo_TextBecomesExtended(t *testing.T) {
	msg := &waE2E.Message{Conversation: proto.String("oi")}
	ci := &waE2E.ContextInfo{StanzaID: proto.String("ABC")}
	applyContextInfo(msg, ci)
	if msg.Conversation != nil {
		t.Fatal("Conversation deveria virar nil ao anexar contexto")
	}
	if msg.GetExtendedTextMessage().GetText() != "oi" {
		t.Errorf("texto perdido: %q", msg.GetExtendedTextMessage().GetText())
	}
	if msg.GetExtendedTextMessage().GetContextInfo().GetStanzaID() != "ABC" {
		t.Error("StanzaID não anexado ao ExtendedTextMessage")
	}
}

func TestApplyContextInfo_Image(t *testing.T) {
	msg := &waE2E.Message{ImageMessage: &waE2E.ImageMessage{Caption: proto.String("foto")}}
	applyContextInfo(msg, &waE2E.ContextInfo{MentionedJID: []string{"x@s.whatsapp.net"}})
	if len(msg.GetImageMessage().GetContextInfo().GetMentionedJID()) != 1 {
		t.Error("menção não anexada à imagem")
	}
}

func TestApplyContextInfo_NilNoop(t *testing.T) {
	msg := &waE2E.Message{Conversation: proto.String("oi")}
	applyContextInfo(msg, nil)
	if msg.Conversation == nil {
		t.Error("contexto nil não deveria alterar a mensagem")
	}
}

func TestIsRevokeMessage(t *testing.T) {
	revoke := &waE2E.Message{ProtocolMessage: &waE2E.ProtocolMessage{
		Type: waE2E.ProtocolMessage_REVOKE.Enum(),
		Key:  &waCommon.MessageKey{ID: proto.String("ORIG")},
	}}
	if !isRevokeMessage(revoke) {
		t.Error("revoke não reconhecido")
	}
	if isRevokeMessage(&waE2E.Message{Conversation: proto.String("oi")}) {
		t.Error("texto comum não é revoke")
	}
	edit := &waE2E.Message{ProtocolMessage: &waE2E.ProtocolMessage{
		Type: waE2E.ProtocolMessage_MESSAGE_EDIT.Enum(),
	}}
	if isRevokeMessage(edit) {
		t.Error("edição não deve ser tratada como revoke")
	}
}

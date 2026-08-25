package main

import (
	"strings"
	"testing"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"google.golang.org/protobuf/proto"
)

// adMessage monta uma extendedTextMessage com ExternalAdReplyInfo, como chega a
// primeira resposta a um anúncio Click to WhatsApp.
func adMessage() *waE2E.Message {
	return &waE2E.Message{
		ExtendedTextMessage: &waE2E.ExtendedTextMessage{
			Text: proto.String("Oi, vi o anúncio"),
			ContextInfo: &waE2E.ContextInfo{
				ExternalAdReply: &waE2E.ContextInfo_ExternalAdReplyInfo{
					Title:      proto.String("Promoção de inverno"),
					Body:       proto.String("50% off"),
					SourceType: proto.String("ad"),
					SourceID:   proto.String("120210000000000000"),
					SourceURL:  proto.String("https://fb.me/abc"),
					CtwaClid:   proto.String("ARzYABCDEF123"),
					Ref:        proto.String("campanha_x"),
				},
			},
		},
	}
}

func TestMessageReferral_Ad(t *testing.T) {
	ref := messageReferral(adMessage())
	if ref == nil {
		t.Fatal("esperava referral de anúncio")
	}
	if ref["ctwaClid"] != "ARzYABCDEF123" {
		t.Errorf("ctwaClid = %v", ref["ctwaClid"])
	}
	if ref["sourceUrl"] != "https://fb.me/abc" {
		t.Errorf("sourceUrl = %v", ref["sourceUrl"])
	}
	if ref["ref"] != "campanha_x" {
		t.Errorf("ref = %v", ref["ref"])
	}
}

func TestMessageReferral_None(t *testing.T) {
	plain := &waE2E.Message{Conversation: proto.String("mensagem normal")}
	if ref := messageReferral(plain); ref != nil {
		t.Errorf("mensagem sem anúncio não deveria ter referral, veio %v", ref)
	}
}

func TestFormatReferralNote(t *testing.T) {
	note := formatReferralNote(messageReferral(adMessage()))
	if !strings.Contains(note, "Click to WhatsApp") {
		t.Errorf("nota sem cabeçalho: %q", note)
	}
	if !strings.Contains(note, "ARzYABCDEF123") {
		t.Errorf("nota sem ctwa_clid: %q", note)
	}
	if formatReferralNote(nil) != "" {
		t.Error("referral nil deveria dar nota vazia")
	}
}

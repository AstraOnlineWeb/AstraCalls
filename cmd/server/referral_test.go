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

// #202: link wa.me/busca trazem entryPointSource sem anúncio — não pode virar
// "Contato veio de um anúncio".
func TestFormatReferralNote_NotAd(t *testing.T) {
	cases := map[string]string{
		"click_to_chat_link":     "Origem: link do WhatsApp",
		"phone_number_hyperlink": "Origem: link de número de telefone",
		"contact_search":         "Origem: busca de contato",
		"global_search_new_chat": "Origem: busca do WhatsApp",
		"algo_novo":              "Origem não identificada* (algo_novo)",
	}
	for src, want := range cases {
		m := &waE2E.Message{ExtendedTextMessage: &waE2E.ExtendedTextMessage{
			Text:        proto.String("oi"),
			ContextInfo: &waE2E.ContextInfo{EntryPointConversionSource: proto.String(src)},
		}}
		note := formatReferralNote(messageReferral(m))
		if strings.Contains(note, "anúncio") {
			t.Errorf("%s: rotulado como anúncio: %q", src, note)
		}
		if !strings.Contains(note, want) {
			t.Errorf("%s: esperava %q em %q", src, want, note)
		}
	}
}

// ExternalAdReply sem ctwa_clid nem sourceType "ad" (ex.: prévia de link de
// outro app) e sem origem: nenhuma nota.
func TestFormatReferralNote_ExternalReplyWithoutAd(t *testing.T) {
	m := &waE2E.Message{ExtendedTextMessage: &waE2E.ExtendedTextMessage{
		Text: proto.String("veja"),
		ContextInfo: &waE2E.ContextInfo{ExternalAdReply: &waE2E.ContextInfo_ExternalAdReplyInfo{
			Title:     proto.String("Página"),
			SourceURL: proto.String("https://exemplo.com"),
			SourceID:  proto.String("123"),
		}},
	}}
	if note := formatReferralNote(messageReferral(m)); note != "" {
		t.Errorf("não deveria gerar nota: %q", note)
	}
}

// anúncio reconhecido só pelo sourceType (sem ctwa_clid) continua anúncio.
func TestFormatReferralNote_AdBySourceType(t *testing.T) {
	ref := map[string]any{"sourceType": "ad", "title": "Promo", "entryPointSource": "ctwa_ad"}
	if note := formatReferralNote(ref); !strings.Contains(note, "Click to WhatsApp") {
		t.Errorf("sourceType=ad deveria ser anúncio: %q", note)
	}
}

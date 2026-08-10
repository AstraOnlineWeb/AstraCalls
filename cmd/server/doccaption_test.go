package main

import (
	"testing"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"google.golang.org/protobuf/proto"
)

// documento COM legenda (documentMessage puro, como o whatsmeow entrega ao vivo
// após desembrulhar o wrapper): a legenda precisa chegar no texto.
func TestDocumentCaption(t *testing.T) {
	m := &waE2E.Message{
		DocumentMessage: &waE2E.DocumentMessage{
			FileName: proto.String("proposta.pdf"),
			Mimetype: proto.String("application/pdf"),
			Caption:  proto.String("segue a proposta"),
		},
	}
	if txt := messageText(m); txt != "segue a proposta" {
		t.Fatalf("messageText = %q, esperava a legenda do documento", txt)
	}
	if typ := messageType(m); typ != "document" {
		t.Fatalf("messageType = %q, esperava document", typ)
	}
	if downloadableOf(m) == nil {
		t.Fatal("downloadableOf devolveu nil p/ documento")
	}
	if fn, mime := mediaMeta(m); fn != "proposta.pdf" || mime != "application/pdf" {
		t.Fatalf("mediaMeta = %q/%q, esperava proposta.pdf/application/pdf", fn, mime)
	}
}

// documento COM legenda embrulhado (documentWithCaptionMessage) — caso do
// HistorySync, que entrega a mensagem crua sem o unwrap do whatsmeow. Nossos
// extratores precisam desembrulhar e não perder arquivo nem legenda.
func TestDocumentWithCaptionWrapper(t *testing.T) {
	m := &waE2E.Message{
		DocumentWithCaptionMessage: &waE2E.FutureProofMessage{
			Message: &waE2E.Message{
				DocumentMessage: &waE2E.DocumentMessage{
					FileName: proto.String("contrato.docx"),
					Mimetype: proto.String("application/vnd.openxmlformats-officedocument.wordprocessingml.document"),
					Caption:  proto.String("contrato assinado"),
				},
			},
		},
	}
	if txt := messageText(m); txt != "contrato assinado" {
		t.Fatalf("messageText (wrapper) = %q, esperava a legenda", txt)
	}
	if typ := messageType(m); typ != "document" {
		t.Fatalf("messageType (wrapper) = %q, esperava document", typ)
	}
	if downloadableOf(m) == nil {
		t.Fatal("downloadableOf (wrapper) devolveu nil — arquivo se perderia")
	}
	if fn, _ := mediaMeta(m); fn != "contrato.docx" {
		t.Fatalf("mediaMeta (wrapper) filename = %q, esperava contrato.docx", fn)
	}
}

// SAÍDA: documento com legenda vira documentWithCaptionMessage (formato oficial);
// sem legenda, documentMessage puro. E o que enviamos precisa ser lido de volta
// pelo extrator (mirror/eco no Chatwoot).
func TestDocumentWithCaptionBuilder(t *testing.T) {
	doc := &waE2E.DocumentMessage{
		FileName: proto.String("nota.pdf"), Mimetype: proto.String("application/pdf"),
	}
	m := documentWithCaption(doc, "segue a nota")
	if m.GetDocumentWithCaptionMessage() == nil {
		t.Fatal("com legenda deveria embrulhar em documentWithCaptionMessage")
	}
	if c := m.GetDocumentWithCaptionMessage().GetMessage().GetDocumentMessage().GetCaption(); c != "segue a nota" {
		t.Fatalf("caption interno = %q, esperava 'segue a nota'", c)
	}
	// round-trip: o extrator de entrada (mirror/eco) lê a legenda de volta.
	if txt := messageText(m); txt != "segue a nota" {
		t.Fatalf("messageText do enviado = %q, esperava a legenda", txt)
	}

	// sem legenda: documentMessage puro, sem wrapper.
	plain := documentWithCaption(&waE2E.DocumentMessage{FileName: proto.String("x.pdf")}, "")
	if plain.GetDocumentWithCaptionMessage() != nil {
		t.Fatal("sem legenda não deveria embrulhar")
	}
	if plain.GetDocumentMessage() == nil {
		t.Fatal("sem legenda deveria ser documentMessage puro")
	}
}

// documento SEM legenda: texto vazio (só o arquivo é entregue).
func TestDocumentNoCaption(t *testing.T) {
	m := &waE2E.Message{
		DocumentMessage: &waE2E.DocumentMessage{
			FileName: proto.String("boleto.pdf"),
			Mimetype: proto.String("application/pdf"),
		},
	}
	if txt := messageText(m); txt != "" {
		t.Fatalf("messageText = %q, esperava vazio (sem legenda)", txt)
	}
	if typ := messageType(m); typ != "document" {
		t.Fatalf("messageType = %q, esperava document", typ)
	}
}

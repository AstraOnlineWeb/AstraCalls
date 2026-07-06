package main

import (
	"testing"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"google.golang.org/protobuf/proto"
)

// visualização única: a mídia vem embrulhada num FutureProofMessage. Garantimos
// que o unwrap expõe o ImageMessage/AudioMessage interno para os acessores.
func TestUnwrapViewOnce(t *testing.T) {
	inner := &waE2E.Message{
		ImageMessage: &waE2E.ImageMessage{
			Caption:  proto.String("segredo"),
			Mimetype: proto.String("image/jpeg"),
			ViewOnce: proto.Bool(true),
		},
	}
	m := &waE2E.Message{
		ViewOnceMessageV2: &waE2E.FutureProofMessage{Message: inner},
	}

	got, viewOnce := unwrapViewOnce(m)
	if !viewOnce {
		t.Fatal("esperava viewOnce=true")
	}
	if got.GetImageMessage() == nil {
		t.Fatal("unwrap não expôs o ImageMessage interno")
	}
	if typ := messageType(m); typ != "image" {
		t.Fatalf("messageType = %q, esperava image", typ)
	}
	if txt := messageText(m); txt != "segredo" {
		t.Fatalf("messageText = %q, esperava segredo", txt)
	}
	if dl := downloadableOf(m); dl == nil {
		t.Fatal("downloadableOf devolveu nil p/ view-once")
	}
	if fname, mime := mediaMeta(m); fname != "image.jpg" || mime != "image/jpeg" {
		t.Fatalf("mediaMeta = %q/%q, esperava image.jpg/image/jpeg", fname, mime)
	}
}

// áudio de visualização única (PTT) chega no V2Extension.
func TestUnwrapViewOnceAudio(t *testing.T) {
	m := &waE2E.Message{
		ViewOnceMessageV2Extension: &waE2E.FutureProofMessage{
			Message: &waE2E.Message{
				AudioMessage: &waE2E.AudioMessage{
					Mimetype: proto.String("audio/ogg; codecs=opus"),
					ViewOnce: proto.Bool(true),
					PTT:      proto.Bool(true),
				},
			},
		},
	}
	if typ := messageType(m); typ != "audio" {
		t.Fatalf("messageType = %q, esperava audio", typ)
	}
	if _, viewOnce := unwrapViewOnce(m); !viewOnce {
		t.Fatal("esperava viewOnce=true no áudio")
	}
}

// mensagem normal (sem view-once) passa reto e viewOnce=false.
func TestUnwrapViewOnceNoop(t *testing.T) {
	m := &waE2E.Message{Conversation: proto.String("oi")}
	got, viewOnce := unwrapViewOnce(m)
	if viewOnce {
		t.Fatal("esperava viewOnce=false p/ mensagem normal")
	}
	if got != m {
		t.Fatal("unwrap deveria devolver a própria mensagem")
	}
}

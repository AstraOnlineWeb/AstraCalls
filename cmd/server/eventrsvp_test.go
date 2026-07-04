package main

import (
	"bytes"
	"testing"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/util/gcmutil"
)

// Valida a consistência interna da cripto de RSVP (a mesma derivação HKDF+AAD
// no encrypt e no decrypt). Não valida contra o WhatsApp (isso é teste ao vivo).
func TestEventSecretRoundtrip(t *testing.T) {
	secret := make([]byte, 32)
	for i := range secret {
		secret[i] = byte(i + 1)
	}
	origSender, _ := types.ParseJID("5561999999999@s.whatsapp.net")
	modSender, _ := types.ParseJID("5561988888888@s.whatsapp.net")
	origID := "3EB0ABC123"

	key, ad := eventSecretKey(modSender, origID, origSender, secret)
	if len(key) != 32 {
		t.Fatalf("chave deveria ter 32 bytes, tem %d", len(key))
	}
	iv := make([]byte, 12)
	plain := []byte("resposta-de-evento")

	ct, err := gcmutil.Encrypt(key, iv, plain, ad)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	key2, ad2 := eventSecretKey(modSender, origID, origSender, secret)
	got, err := gcmutil.Decrypt(key2, iv, ct, ad2)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if !bytes.Equal(got, plain) {
		t.Fatalf("roundtrip falhou: %q != %q", got, plain)
	}
	// AAD errado deve falhar a autenticação
	if _, e := gcmutil.Decrypt(key2, iv, ct, []byte("aad-errado")); e == nil {
		t.Fatal("decrypt com AAD errado deveria falhar")
	}
}

func TestParseEventResponse(t *testing.T) {
	cases := map[string]waE2E.EventResponseMessage_EventResponseType{
		"going": waE2E.EventResponseMessage_GOING,
		"vai":   waE2E.EventResponseMessage_GOING,
		"maybe": waE2E.EventResponseMessage_MAYBE,
		"talvez": waE2E.EventResponseMessage_MAYBE,
		"not_going": waE2E.EventResponseMessage_NOT_GOING,
		"não vai":   waE2E.EventResponseMessage_NOT_GOING,
	}
	for in, want := range cases {
		if got, ok := parseEventResponse(in); !ok || got != want {
			t.Fatalf("parseEventResponse(%q) = %v,%v; queria %v", in, got, ok, want)
		}
	}
	if _, ok := parseEventResponse("xyz"); ok {
		t.Fatal("resposta inválida deveria falhar")
	}
}

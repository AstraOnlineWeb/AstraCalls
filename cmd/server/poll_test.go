package main

import (
	"crypto/sha256"
	"strings"
	"testing"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"google.golang.org/protobuf/proto"
)

func TestMatchVoteOptions(t *testing.T) {
	options := []string{"Manhã", "Tarde", "Noite"}
	// simula um voto em "Tarde" e "Noite" (só os hashes, como vem do WhatsApp)
	h1 := sha256.Sum256([]byte("Tarde"))
	h2 := sha256.Sum256([]byte("Noite"))
	got := matchVoteOptions([][]byte{h1[:], h2[:]}, options)
	if len(got) != 2 || got[0] != "Tarde" || got[1] != "Noite" {
		t.Fatalf("esperava [Tarde Noite], veio %v", got)
	}
	if v := pollVoteText("João", got); !strings.Contains(v, "João votou: Tarde, Noite") {
		t.Fatalf("pollVoteText inesperado: %s", v)
	}
	if v := pollVoteText("João", nil); !strings.Contains(v, "retirou o voto") {
		t.Fatalf("voto vazio inesperado: %s", v)
	}
}

func TestPollText(t *testing.T) {
	pm := &waE2E.PollCreationMessage{
		Name:                   proto.String("Melhor horário?"),
		SelectableOptionsCount: proto.Uint32(2),
		Options: []*waE2E.PollCreationMessage_Option{
			{OptionName: proto.String("Manhã")},
			{OptionName: proto.String("Tarde")},
			{OptionName: proto.String("Noite")},
		},
	}
	got := pollText(pm)
	for _, want := range []string{"Melhor horário?", "1. Manhã", "2. Tarde", "3. Noite", "escolha até 2"} {
		if !strings.Contains(got, want) {
			t.Fatalf("pollText não contém %q. Saída:\n%s", want, got)
		}
	}
	// cobre os vários envelopes de versão
	for _, msg := range []*waE2E.Message{
		{PollCreationMessage: pm},
		{PollCreationMessageV3: pm},
		{PollCreationMessageV6: pm},
	} {
		if messageType(msg) != "poll" {
			t.Fatal("messageType deveria ser 'poll'")
		}
	}
}

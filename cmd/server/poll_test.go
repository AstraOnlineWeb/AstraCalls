package main

import (
	"strings"
	"testing"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"google.golang.org/protobuf/proto"
)

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

package main

import (
	"fmt"
	"strings"

	"go.mau.fi/whatsmeow/proto/waE2E"
)

// getPoll devolve a enquete (PollCreationMessage) de qualquer versão do envelope
// (V1..V6 são o mesmo struct; V4 é FutureProof e não é coberto aqui).
func getPoll(m *waE2E.Message) *waE2E.PollCreationMessage {
	for _, p := range []*waE2E.PollCreationMessage{
		m.GetPollCreationMessage(),
		m.GetPollCreationMessageV2(),
		m.GetPollCreationMessageV3(),
		m.GetPollCreationMessageV5(),
		m.GetPollCreationMessageV6(),
	} {
		if p != nil {
			return p
		}
	}
	return nil
}

// pollText formata uma enquete recebida (pergunta + opções numeradas).
func pollText(p *waE2E.PollCreationMessage) string {
	if p == nil {
		return ""
	}
	name := p.GetName()
	if name == "" {
		name = "Enquete"
	}
	var b strings.Builder
	b.WriteString("📊 *Enquete: " + name + "*")
	for i, opt := range p.GetOptions() {
		b.WriteString(fmt.Sprintf("\n%d. %s", i+1, opt.GetOptionName()))
	}
	if n := p.GetSelectableOptionsCount(); n > 1 {
		b.WriteString(fmt.Sprintf("\n_(escolha até %d opções)_", n))
	}
	return b.String()
}

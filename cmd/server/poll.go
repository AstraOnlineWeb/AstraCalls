package main

import (
	"bytes"
	"crypto/sha256"
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

// matchVoteOptions mapeia os hashes SHA-256 de um voto de volta para os nomes
// das opções da enquete (o voto vem só com os hashes).
func matchVoteOptions(selected [][]byte, options []string) []string {
	out := []string{}
	for _, name := range options {
		h := sha256.Sum256([]byte(name))
		for _, sel := range selected {
			if bytes.Equal(sel, h[:]) {
				out = append(out, name)
				break
			}
		}
	}
	return out
}

// pollVoteText formata um voto recebido (quem votou + opções escolhidas).
func pollVoteText(voter string, names []string) string {
	if len(names) == 0 {
		return "🗳️ " + voter + " retirou o voto na enquete"
	}
	return "🗳️ " + voter + " votou: " + strings.Join(names, ", ")
}

// pollOptionNames extrai os nomes das opções de uma enquete guardada (raw).
func pollOptionNames(p *waE2E.PollCreationMessage) []string {
	out := make([]string, 0, len(p.GetOptions()))
	for _, o := range p.GetOptions() {
		out = append(out, o.GetOptionName())
	}
	return out
}

package main

import (
	"fmt"
	"strings"

	"go.mau.fi/whatsmeow/proto/waE2E"
)

// vcardPhones extrai os telefones (linhas TEL) de um vCard.
func vcardPhones(vcard string) []string {
	var phones []string
	for _, line := range strings.Split(vcard, "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.HasPrefix(strings.ToUpper(line), "TEL") {
			if i := strings.Index(line, ":"); i >= 0 {
				if v := strings.TrimSpace(line[i+1:]); v != "" {
					phones = append(phones, v)
				}
			}
		}
	}
	return phones
}

// contactText formata um contato (vCard) recebido.
func contactText(cm *waE2E.ContactMessage) string {
	if cm == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString("👤 *Contato")
	if name := cm.GetDisplayName(); name != "" {
		b.WriteString(": " + name)
	}
	b.WriteString("*")
	for _, p := range vcardPhones(cm.GetVcard()) {
		b.WriteString("\n📞 " + p)
	}
	return b.String()
}

// contactsArrayText formata uma lista de contatos compartilhados.
func contactsArrayText(cam *waE2E.ContactsArrayMessage) string {
	if cam == nil {
		return ""
	}
	contacts := cam.GetContacts()
	var b strings.Builder
	b.WriteString(fmt.Sprintf("👤 *%d contatos compartilhados*", len(contacts)))
	for _, c := range contacts {
		line := "\n• " + c.GetDisplayName()
		if phones := vcardPhones(c.GetVcard()); len(phones) > 0 {
			line += " — " + strings.Join(phones, ", ")
		}
		b.WriteString(line)
	}
	return b.String()
}

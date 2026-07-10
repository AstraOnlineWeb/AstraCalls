package main

import (
	"context"
	"strings"

	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

// handleGroupSystemEvent trata as mudanças de participantes de um grupo (entrar,
// sair, virar/deixar de ser admin) — os mesmos avisos que o WhatsApp mostra na
// janela do grupo. Dispara o webhook "group_participants" e, se a integração
// Chatwoot com grupos estiver ligada, posta uma NOTA PRIVADA informativa na
// conversa daquele grupo.
func (s *Session) handleGroupSystemEvent(evt *events.GroupInfo) {
	// só nos interessam mudanças de participantes/cargo (ignora rename/topic/etc.)
	if len(evt.Join) == 0 && len(evt.Leave) == 0 && len(evt.Promote) == 0 && len(evt.Demote) == 0 {
		return
	}
	s.dispatchWebhook("group_participants", s.groupSystemPayload(evt))

	cfg := s.getChatwoot()
	if !cfg.valid() || !cfg.Groups {
		return
	}
	lines := s.groupSystemLines(evt)
	if len(lines) == 0 {
		return
	}
	convID, err := s.groupConversation(cfg, evt.JID)
	if err != nil {
		s.log.Error("chatwoot: group system event conversation failed", "err", err)
		return
	}
	if err := cfg.postText(convID, strings.Join(lines, "\n"), true, "", ""); err != nil {
		s.log.Error("chatwoot: post group system note failed", "err", err)
	}
}

// groupSystemLines monta as linhas legíveis (uma por mudança).
func (s *Session) groupSystemLines(evt *events.GroupInfo) []string {
	var lines []string
	for _, j := range evt.Join {
		lines = append(lines, "➕ "+s.participantLabel(j)+" entrou no grupo")
	}
	for _, j := range evt.Leave {
		lines = append(lines, "➖ "+s.participantLabel(j)+" saiu do grupo")
	}
	for _, j := range evt.Promote {
		lines = append(lines, "⭐ "+s.participantLabel(j)+" agora é administrador")
	}
	for _, j := range evt.Demote {
		lines = append(lines, "⬇️ "+s.participantLabel(j)+" deixou de ser administrador")
	}
	return lines
}

// groupSystemPayload monta o corpo do webhook com os JIDs de cada mudança.
func (s *Session) groupSystemPayload(evt *events.GroupInfo) map[string]any {
	jids := func(list []types.JID) []string {
		out := make([]string, 0, len(list))
		for _, j := range list {
			out = append(out, j.String())
		}
		return out
	}
	p := map[string]any{
		"group":     evt.JID.String(),
		"timestamp": evt.Timestamp.UnixMilli(),
	}
	if evt.Sender != nil {
		p["actor"] = evt.Sender.String()
	}
	if len(evt.Join) > 0 {
		p["joined"] = jids(evt.Join)
	}
	if len(evt.Leave) > 0 {
		p["left"] = jids(evt.Leave)
	}
	if len(evt.Promote) > 0 {
		p["promoted"] = jids(evt.Promote)
	}
	if len(evt.Demote) > 0 {
		p["demoted"] = jids(evt.Demote)
	}
	return p
}

// participantLabel resolve um nome amigável (contato) + telefone para um JID.
func (s *Session) participantLabel(jid types.JID) string {
	phone := s.realPhone(jid)
	if c, err := s.client.Store.Contacts.GetContact(context.Background(), jid); err == nil {
		if n := firstNonEmptyOf(c.FullName, c.PushName, c.FirstName, c.BusinessName); n != "" {
			if phone != "" && phone != n {
				return n + " (+" + phone + ")"
			}
			return n
		}
	}
	if phone != "" {
		return "+" + phone
	}
	return jid.User
}

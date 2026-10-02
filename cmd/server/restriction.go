package main

import (
	"context"
	"net/http"
	"strings"
	"time"

	"wacalls/internal/wa"

	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

// Restrição / shadow-ban (erro 463 "time-lock"): o WhatsApp limita o alcance do
// número — mensagens/chamadas para quem ainda não respondeu "somem" em silêncio
// (sem erro no envio). O ÚNICO sinal programático é o evento
// NotifyAccountReachoutTimelock, que o WhatsApp EMPURRA (não há consulta). Guardamos
// o último estado em cache na sessão, avisamos (webhook + Chatwoot) e expomos num
// endpoint de status.

// handleReachoutTimelock trata o push de restrição: atualiza o cache, dispara o
// webhook "restriction" e, quando a restrição fica ativa, alerta UMA vez no Chatwoot.
func (s *Session) handleReachoutTimelock(evt *events.NotifyAccountReachoutTimelock) {
	ends := evt.TimeEnforcementEnds.Time

	s.mu.Lock()
	s.restrictionActive = evt.IsActive
	s.restrictionType = evt.EnforcementType
	s.restrictionEndsAt = ends
	s.restrictionUpdatedAt = time.Now()
	alerted := s.restrictionAlerted
	if !evt.IsActive {
		s.restrictionAlerted = false // restrição saiu: rearma o alerta p/ a próxima
	}
	s.mu.Unlock()

	s.log.Warn("WhatsApp impôs restrição/time-lock (shadow-ban 463)",
		"active", evt.IsActive, "type", evt.EnforcementType, "ends", ends)

	s.dispatchWebhook("restriction", map[string]any{
		"active":            evt.IsActive,
		"enforcement_type":  evt.EnforcementType,
		"ends_at":           unixMillisOrZero(ends),
		"seconds_remaining": secondsRemaining(ends),
		"session":           s.id,
		"name":              s.name,
	})

	if evt.IsActive && !alerted {
		s.mu.Lock()
		s.restrictionAlerted = true
		s.mu.Unlock()
		text := "⚠️ *AstraCalls — número com restrição do WhatsApp*\n" +
			"O WhatsApp aplicou uma restrição de alcance (time-lock / anti-spam) na sessão *" + s.name + "*" +
			restrictionWhenText(ends) + ".\n" +
			"Mensagens e chamadas para quem ainda NÃO te respondeu podem não chegar. " +
			"Evite disparos em massa e peça para o contato te responder/salvar o número."
		s.chatwootAlert(text)
	}
}

// handleRestrictionStatus devolve o último estado de restrição em cache. Com
// ?peer=<telefone> também informa se há token de privacidade e mapeamento LID para
// aquele contato (útil pra diagnosticar entrega anti-463). O protocolo não tem
// consulta ativa — só o push —, então o estado é o do último evento recebido.
func (s *server) handleRestrictionStatus(w http.ResponseWriter, r *http.Request) {
	sess := s.sessionByID(w, r.PathValue("sid"))
	if sess == nil {
		return
	}

	sess.mu.Lock()
	rest := map[string]any{
		"active":            sess.restrictionActive,
		"enforcement_type":  sess.restrictionType,
		"ends_at":           unixMillisOrZero(sess.restrictionEndsAt),
		"seconds_remaining": secondsRemaining(sess.restrictionEndsAt),
		"updated_at":        unixMillisOrZero(sess.restrictionUpdatedAt),
	}
	sess.mu.Unlock()

	out := map[string]any{"session": sess.id, "restriction": rest}
	if peer := strings.TrimSpace(r.URL.Query().Get("peer")); peer != "" {
		out["peer"] = sess.peerTokenStatus(r.Context(), peer)
	}
	writeJSON(w, http.StatusOK, out)
}

// peerTokenStatus reporta, para um contato, se temos o LID mapeado e o token de
// privacidade (TC token) — os dois que o WhatsApp exige para não aplicar o 463 no
// envio. O GetPrivacyToken já cruza PN↔LID no SQL quando o lid_map tem o par.
func (s *Session) peerTokenStatus(ctx context.Context, phone string) map[string]any {
	pn := types.NewJID(normalizePhone(phone), types.DefaultUserServer)
	out := map[string]any{"pn": pn.String()}

	lid := wa.NewSocket(s.client).ResolveLIDForPN(ctx, pn)
	if lid.Server == types.HiddenUserServer && !lid.IsEmpty() {
		out["lid"] = lid.String()
		out["lid_mapped"] = true
	} else {
		out["lid_mapped"] = false
	}

	has := false
	if s.client.Store != nil && s.client.Store.PrivacyTokens != nil {
		if tok, err := s.client.Store.PrivacyTokens.GetPrivacyToken(ctx, pn); err == nil && tok != nil && len(tok.Token) > 0 {
			has = true
		}
	}
	out["has_tctoken"] = has
	return out
}

// unixMillisOrZero devolve o epoch em ms, ou 0 quando o tempo é zero.
func unixMillisOrZero(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

// secondsRemaining devolve quantos segundos faltam até t (0 se já passou ou zero).
func secondsRemaining(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	if d := time.Until(t); d > 0 {
		return int64(d.Seconds())
	}
	return 0
}

// restrictionWhenText formata " até DD/MM HH:MM" quando há prazo conhecido.
func restrictionWhenText(ends time.Time) string {
	if ends.IsZero() {
		return ""
	}
	return " até " + ends.Local().Format("02/01 15:04")
}

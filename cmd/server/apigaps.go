package main

import (
	"context"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// ---------------------------------------------------------------------------
// Lacunas de integração pedidas por quem usa a API como camada de WhatsApp de
// uma plataforma de atendimento (Inbox): download de mídia recebida, citação e
// menções no envio, evento de mensagem apagada (revoke), telefone real junto do
// @lid e tipo do chat. Tudo reaproveita peças que já existiam (Download interno
// usado no Chatwoot, quoteContext, realPhone).
// ---------------------------------------------------------------------------

// chatKind classifica um chat pelo servidor do JID, para o cliente separar
// conversa individual de grupo/canal em /chats (enviar texto p/ canal não é
// possível — o WhatsApp recusa).
func chatKind(chatJID string) string {
	switch {
	case strings.HasSuffix(chatJID, "@g.us"):
		return "group"
	case strings.HasSuffix(chatJID, "@newsletter"):
		return "channel"
	case strings.HasSuffix(chatJID, "@broadcast"):
		return "broadcast"
	default:
		return "individual"
	}
}

// handleGetMedia baixa e devolve os BYTES de uma mídia RECEBIDA (imagem/áudio/
// vídeo/documento/sticker) a partir do id da mensagem. A mensagem crua já fica
// guardada (protojson) no store; aqui só desembrulhamos, deciframos via
// whatsmeow (mesma rota usada p/ empurrar ao Chatwoot) e servimos.
// GET /api/sessions/{sid}/messages/{id}/media[?download=1]
func (s *server) handleGetMedia(w http.ResponseWriter, r *http.Request) {
	sess := s.sessionByID(w, r.PathValue("sid"))
	if sess == nil {
		return
	}
	id := r.PathValue("id")
	_, _, _, raw, err := s.sessions.store.findMessage(r.Context(), sess.id, id)
	if err != nil || len(raw) == 0 {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "message not found"})
		return
	}
	var m waE2E.Message
	if err := protojson.Unmarshal(raw, &m); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "decode failed"})
		return
	}
	dl := downloadableOf(&m)
	if dl == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "message has no media"})
		return
	}
	data, err := sess.client.Download(r.Context(), dl)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	fname, mime := mediaMeta(&m)
	disp := "inline"
	if v := r.URL.Query().Get("download"); v == "1" || v == "true" {
		disp = "attachment"
	}
	w.Header().Set("Content-Type", mime)
	w.Header().Set("Content-Disposition", disp+`; filename="`+fname+`"`)
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

// mentionJID normaliza uma menção (número ou JID) para o JID de usuário que o
// WhatsApp espera em contextInfo.mentionedJid.
func mentionJID(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	if strings.Contains(v, "@") {
		return v
	}
	return types.NewJID(normalizePhone(v), types.DefaultUserServer).String()
}

// mentionRe acha "@<8..15 dígitos>" no texto — a forma como o atendente marca
// alguém no Chatwoot/AstraChat (ele não tem como preencher contextInfo).
var mentionRe = regexp.MustCompile(`@\+?(\d{8,15})\b`)

// detectMentions extrai os números mencionados (@5561...) do texto, sem repetir.
func detectMentions(text string) []string {
	var out []string
	seen := map[string]bool{}
	for _, m := range mentionRe.FindAllStringSubmatch(text, -1) {
		d := m[1]
		if !seen[d] {
			seen[d] = true
			out = append(out, d)
		}
	}
	return out
}

// mentionTargets converte números/JIDs mencionados nos JIDs que vão em
// contextInfo.mentionedJid. Em grupos endereçados por LID o aparelho só marca se o
// LID estiver na lista, e em grupos por PN só o PN; como não sabemos de antemão,
// mandamos os DOIS quando o store conhece o par PN↔LID (entradas sobrando são
// ignoradas pelo WhatsApp). Dígitos que são um LID conhecido viram <lid>@lid.
func (s *Session) mentionTargets(ctx context.Context, mentions []string) []string {
	var out []string
	seen := map[string]bool{}
	add := func(j types.JID) {
		if j.IsEmpty() {
			return
		}
		k := j.ToNonAD().String()
		if !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	for _, m := range mentions {
		m = strings.TrimSpace(m)
		if m == "" {
			continue
		}
		if strings.Contains(m, "@") {
			if j, err := types.ParseJID(m); err == nil {
				add(j)
			}
			continue
		}
		digits := normalizePhone(m)
		if digits == "" {
			continue
		}
		// os dígitos são um LID conhecido? (lista do AstraChat mostra o LID quando
		// não há telefone; o atendente marca "@<lid>")
		asLID := types.NewJID(digits, types.HiddenUserServer)
		if pn, err := s.client.Store.LIDs.GetPNForLID(ctx, asLID); err == nil && pn.User != "" {
			add(asLID)
			add(pn)
			continue
		}
		pn := types.NewJID(digits, types.DefaultUserServer)
		add(pn)
		if lid, err := s.client.Store.LIDs.GetLIDForPN(ctx, pn); err == nil && lid.User != "" {
			add(lid)
		}
	}
	return out
}

// autoMentions decide as menções de um envio: as explícitas do body ou, quando
// o destino é um GRUPO e nada foi informado, as detectadas no texto ("@5561...").
// Fora de grupo não há menção.
func (s *Session) autoMentions(ctx context.Context, to string, text string, explicit []string) []string {
	if len(explicit) > 0 {
		return explicit
	}
	jid, err := resolveRecipient(to)
	if err != nil || jid.Server != types.GroupServer {
		return nil
	}
	return detectMentions(text)
}

// buildSendContext monta o ContextInfo de uma mensagem de SAÍDA para citação
// (quotedMessageId) e/ou menções (@). Para a citação, resolve o remetente e o
// conteúdo da mensagem original pelo store (como o quoteContext do Chatwoot).
// Devolve nil quando não há nada a anexar.
func (s *Session) buildSendContext(ctx context.Context, quotedID, participant string, fromMe bool, mentions []string) *waE2E.ContextInfo {
	var ci *waE2E.ContextInfo
	if q := strings.TrimSpace(quotedID); q != "" {
		ci = &waE2E.ContextInfo{StanzaID: proto.String(q)}
		p := strings.TrimSpace(participant)
		_, senderStr, storedFromMe, raw, err := s.mgr.store.findMessage(ctx, s.id, q)
		if err == nil {
			if p == "" {
				switch {
				case senderStr != "":
					p = senderStr
				case (fromMe || storedFromMe) && s.client.Store.ID != nil:
					p = s.client.Store.ID.ToNonAD().String()
				}
			}
			if len(raw) > 0 {
				var qm waE2E.Message
				if protojson.Unmarshal(raw, &qm) == nil {
					ci.QuotedMessage = &qm
				}
			}
		}
		if p != "" {
			ci.Participant = proto.String(mentionJID(p))
		}
	}
	if len(mentions) > 0 {
		jids := s.mentionTargets(ctx, mentions)
		if len(jids) > 0 {
			if ci == nil {
				ci = &waE2E.ContextInfo{}
			}
			ci.MentionedJID = jids
		}
	}
	return ci
}

// applyContextInfo anexa o ContextInfo à sub-mensagem correspondente. Texto
// simples (Conversation) vira ExtendedTextMessage para poder carregar o
// contexto (citação/menção).
func applyContextInfo(msg *waE2E.Message, ci *waE2E.ContextInfo) {
	if ci == nil || msg == nil {
		return
	}
	switch {
	case msg.ExtendedTextMessage != nil:
		msg.ExtendedTextMessage.ContextInfo = ci
	case msg.Conversation != nil:
		msg.ExtendedTextMessage = &waE2E.ExtendedTextMessage{Text: msg.Conversation, ContextInfo: ci}
		msg.Conversation = nil
	case msg.ImageMessage != nil:
		msg.ImageMessage.ContextInfo = ci
	case msg.VideoMessage != nil:
		msg.VideoMessage.ContextInfo = ci
	case msg.AudioMessage != nil:
		msg.AudioMessage.ContextInfo = ci
	case msg.DocumentMessage != nil:
		msg.DocumentMessage.ContextInfo = ci
	case msg.StickerMessage != nil:
		msg.StickerMessage.ContextInfo = ci
	case msg.LocationMessage != nil:
		msg.LocationMessage.ContextInfo = ci
	case msg.ContactMessage != nil:
		msg.ContactMessage.ContextInfo = ci
	}
}

// messagePayload é o summarizeMessage enriquecido com o telefone real (PN) do
// remetente e do chat, já que os JIDs chegam como @lid (sem o telefone). Assim
// a integração não precisa remapear pela lista de contatos.
func (s *Session) messagePayload(evt *events.Message) map[string]any {
	out := summarizeMessage(evt)
	if p := s.realPhone(evt.Info.Sender); p != "" {
		out["senderPhone"] = p
	}
	if !evt.Info.IsGroup && evt.Info.Chat.Server != types.NewsletterServer {
		if p := s.realPhone(evt.Info.Chat); p != "" {
			out["chatPhone"] = p
		}
	}
	// telefone real (PN) de quem enviou a mensagem citada (o quotedParticipant
	// chega como @lid); ajuda a integração a identificar o autor da citada.
	if qp, ok := out["quotedParticipant"].(string); ok && qp != "" {
		if jid, err := types.ParseJID(qp); err == nil {
			if p := s.realPhone(jid); p != "" {
				out["quotedParticipantPhone"] = p
			}
		}
	}
	return out
}

// isRevokeMessage diz se a mensagem recebida é uma EXCLUSÃO (revoke) de outra.
// ATENÇÃO: o valor zero do enum ProtocolMessage_Type é justamente REVOKE, então é
// obrigatório checar o ProtocolMessage != nil — senão QUALQUER mensagem de texto
// (sem ProtocolMessage) seria classificada como exclusão.
func isRevokeMessage(m *waE2E.Message) bool {
	pm := m.GetProtocolMessage()
	return pm != nil && pm.GetType() == waE2E.ProtocolMessage_REVOKE
}

// handleIncomingRevoke emite o evento `deleted` quando o contato apaga uma
// mensagem para todos. O id é o da mensagem ORIGINAL apagada (= source_id no
// Chatwoot), para a integração marcar/remover a mensagem certa.
func (s *Session) handleIncomingRevoke(evt *events.Message) {
	origID := evt.Message.GetProtocolMessage().GetKey().GetID()
	if origID == "" {
		return
	}
	s.dispatchWebhook("deleted", map[string]any{
		"id":          origID,
		"chat":        evt.Info.Chat.String(),
		"author":      evt.Info.Sender.String(),
		"authorPhone": s.realPhone(evt.Info.Sender),
		"fromMe":      evt.Info.IsFromMe,
		"isGroup":     evt.Info.IsGroup,
		"timestamp":   evt.Info.Timestamp.UnixMilli(),
	})
}

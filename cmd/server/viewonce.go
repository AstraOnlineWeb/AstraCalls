package main

import (
	"context"

	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

// handleUnavailableViewOnce trata o placeholder <unavailable type="view_once">.
// O WhatsApp NÃO entrega o conteúdo de visualização única para dispositivos
// vinculados (a API é um): manda só um aviso de indisponível e, mesmo quando
// pedimos o reenvio pro celular, a política dele bloqueia o envio da mídia. Como
// não dá pra exibir o conteúdo, ao menos avisamos o atendente no Chatwoot (e via
// webhook) que o cliente enviou uma visualização única.
func (s *Session) handleUnavailableViewOnce(evt *events.UndecryptableMessage) {
	s.dispatchWebhook("view_once", map[string]any{
		"id":          evt.Info.ID,
		"chat":        evt.Info.Chat.String(),
		"sender":      evt.Info.Sender.String(),
		"senderPhone": s.realPhone(evt.Info.Sender),
		"pushName":    evt.Info.PushName,
		"timestamp":   evt.Info.Timestamp.UnixMilli(),
		"available":   false, // WhatsApp não libera view-once p/ dispositivos vinculados
	})

	if evt.Info.IsFromMe {
		return
	}
	cfg := s.getChatwoot()
	if !cfg.valid() {
		return
	}
	convID, prefix, ok := s.viewOnceConversation(cfg, &evt.Info)
	if !ok {
		return
	}
	notice := prefix + "👁️ _Visualização única_\n_O cliente enviou uma mídia de visualização única. O WhatsApp não libera esse conteúdo para dispositivos conectados (API), então não é possível exibi-la aqui._"
	if err := cfg.postText(convID, notice, cwIncoming, "", "", 0); err != nil {
		s.log.Error("chatwoot: aviso de view-once falhou", "err", err)
	}
}

// viewOnceConversation resolve (contato+conversa) e o prefixo de autor a partir
// do MessageInfo, seguindo o mesmo roteamento de chatwootPushIncoming (1:1, grupo
// e canal). Devolve ok=false quando o tipo não está habilitado/suportado.
func (s *Session) viewOnceConversation(cfg ChatwootConfig, info *types.MessageInfo) (int, string, bool) {
	var chatID, phone, name, prefix, avatar string
	switch info.Chat.Server {
	case types.DefaultUserServer, types.HiddenUserServer:
		chat := info.Chat
		phone = chat.User
		if chat.Server != types.DefaultUserServer {
			if info.SenderAlt.Server == types.DefaultUserServer && info.SenderAlt.User != "" {
				phone = info.SenderAlt.User
			} else {
				phone = s.realPhone(chat)
			}
		}
		chatID = phone + "@" + types.DefaultUserServer
		name = info.PushName
		if name == "" {
			name = phone
		}
		if pp, perr := s.client.GetProfilePictureInfo(context.Background(), info.Chat, nil); perr == nil && pp != nil {
			avatar = pp.URL
		}
	case types.GroupServer:
		if !cfg.Groups {
			return 0, "", false
		}
		chatID = info.Chat.String()
		name = chatID
		if gi, err := s.client.GetGroupInfo(context.Background(), info.Chat); err == nil && gi.Name != "" {
			name = gi.Name
		}
		if pp, perr := s.client.GetProfilePictureInfo(context.Background(), info.Chat, nil); perr == nil && pp != nil {
			avatar = pp.URL
		}
		author := info.PushName
		if author == "" {
			author = s.realPhone(info.Sender)
		}
		prefix = "*" + author + "*:\n"
	case types.NewsletterServer:
		if !cfg.Channels {
			return 0, "", false
		}
		chatID = info.Chat.String()
		name = chatID
		if ni, err := s.client.GetNewsletterInfo(context.Background(), info.Chat); err == nil && ni.ThreadMeta.Name.Text != "" {
			name = ni.ThreadMeta.Name.Text
		}
		name = "📢 " + name
	default:
		return 0, "", false
	}

	// grupos/canais não têm telefone -> phone == "" (busca por identifier)
	contactID, sourceID, err := cfg.ensureContact(chatID, phone, name, avatar)
	if err != nil {
		s.log.Error("chatwoot: ensure contact (view-once) failed", "err", err)
		return 0, "", false
	}
	convID, err := cfg.ensureConversation(contactID, sourceID)
	if err != nil {
		s.log.Error("chatwoot: ensure conversation (view-once) failed", "err", err)
		return 0, "", false
	}
	return convID, prefix, true
}

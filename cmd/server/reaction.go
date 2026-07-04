package main

import (
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types/events"
)

// Reações recebidas. 1:1 vem como ReactionMessage (texto = emoji, vazio = reação
// removida). Grupos/canais vêm como EncReactionMessage (criptografada) —
// DecryptReaction (público no whatsmeow) resolve. Encaminhamos via webhook
// `reaction` com o ID da mensagem reagida (que é o source_id no Chatwoot), para
// o Chatwoot atrelar a reação à mensagem certa.

func (s *Session) handleIncomingReaction(evt *events.Message) {
	var rm *waE2E.ReactionMessage
	if r := evt.Message.GetReactionMessage(); r != nil {
		rm = r
	} else if evt.Message.GetEncReactionMessage() != nil {
		dec, err := s.client.DecryptReaction(s.mgr.appCtx, evt)
		if err != nil {
			s.log.Warn("decrypt reaction falhou", "err", err)
			return
		}
		rm = dec
	}
	if rm == nil {
		return
	}

	emoji := rm.GetText() // "" = reação removida
	who := evt.Info.PushName
	if who == "" {
		who = s.realPhone(evt.Info.Sender)
	}

	s.dispatchWebhook("reaction", map[string]any{
		"messageId":    rm.GetKey().GetID(), // a msg do WhatsApp reagida (= source_id no Chatwoot)
		"emoji":        emoji,
		"removed":      emoji == "",
		"reactor":      evt.Info.Sender.String(),
		"reactorPhone": s.realPhone(evt.Info.Sender),
		"reactorName":  who,
		"chat":         evt.Info.Chat.String(),
		"toMe":         rm.GetKey().GetFromMe(), // true = reagiram a uma mensagem NOSSA
		"timestamp":    evt.Info.Timestamp.UnixMilli(),
	})
}

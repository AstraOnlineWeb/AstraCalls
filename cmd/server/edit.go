package main

import (
	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

// handleIncomingSecretEdit trata a EDIÇÃO de mensagem recebida. O WhatsApp moderno
// entrega a edição CRIPTOGRAFADA num secretEncryptedMessage (não no protocolMessage
// em texto puro) — por isso chegava como type "unknown"/texto vazio. Decifra com o
// segredo da mensagem-alvo (DecryptSecretEncryptedMessage) e reembrulha como um
// MESSAGE_EDIT em texto puro, para reaproveitar todo o pipeline de edição
// (unwrapEdit -> messageText/webhook edited/editedId -> nota "✏️ Editada" no Chatwoot).
func (s *Session) handleIncomingSecretEdit(evt *events.Message) {
	sec := evt.Message.GetSecretEncryptedMessage()
	if sec == nil {
		return
	}
	// Por ora só edição de MENSAGEM. Edição de enquete/evento (POLL_EDIT/EVENT_EDIT)
	// tem tratamento próprio e fica de fora daqui.
	if sec.GetSecretEncType() != waE2E.SecretEncryptedMessage_MESSAGE_EDIT {
		return
	}
	dec, err := s.client.DecryptSecretEncryptedMessage(s.mgr.appCtx, evt)
	if err != nil {
		s.log.Warn("decrypt edição falhou", "err", err, "id", evt.Info.ID)
		return
	}
	origID := sec.GetTargetMessageKey().GetID()

	// reembrulha o conteúdo decifrado como MESSAGE_EDIT plaintext e roda o mesmo
	// caminho de uma mensagem normal (persistência + webhook + Chatwoot).
	synthetic := *evt
	synthetic.Message = &waE2E.Message{
		ProtocolMessage: &waE2E.ProtocolMessage{
			Type:          waE2E.ProtocolMessage_MESSAGE_EDIT.Enum(),
			Key:           &waCommon.MessageKey{ID: proto.String(origID)},
			EditedMessage: dec,
		},
	}
	s.storeMessageEvent(&synthetic)
	s.dispatchWebhook("message", s.messagePayload(&synthetic))
	s.chatwootPushIncoming(&synthetic)
}

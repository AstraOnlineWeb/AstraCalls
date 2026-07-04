package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"go.mau.fi/util/random"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"go.mau.fi/whatsmeow/util/gcmutil"
	"go.mau.fi/whatsmeow/util/hkdfutil"
	"google.golang.org/protobuf/proto"
)

// Resposta de presença (RSVP) de Evento. O whatsmeow NÃO expõe encrypt/decrypt
// para EventResponse (só para poll/reaction/comment), mas a cripto é a mesma
// (HKDF-SHA256 + AES-GCM sobre o segredo da mensagem, que o whatsmeow guarda
// automaticamente em Store.MsgSecrets ao receber o evento). Aqui replicamos o
// algoritmo usando as primitivas públicas.

// eventSecretKey deriva a chave/AAD para o segredo "Event Response" — espelha o
// generateMsgSecretKey interno do whatsmeow.
func eventSecretKey(modSender types.JID, origMsgID string, origSender types.JID, origSecret []byte) (secretKey, additionalData []byte) {
	origSenderStr := origSender.ToNonAD().String()
	modSenderStr := modSender.ToNonAD().String()
	useCase := string(whatsmeow.EncSecretEventResponse)

	info := make([]byte, 0, len(origMsgID)+len(origSenderStr)+len(modSenderStr)+len(useCase))
	info = append(info, origMsgID...)
	info = append(info, origSenderStr...)
	info = append(info, modSenderStr...)
	info = append(info, useCase...)

	secretKey = hkdfutil.SHA256(origSecret, nil, info, 32)
	additionalData = fmt.Appendf(nil, "%s\x00%s", origMsgID, modSenderStr)
	return
}

// buildEventResponse monta um EncEventResponseMessage (RSVP) para enviar.
func (s *Session) buildEventResponse(ctx context.Context, ev *types.MessageInfo, resp waE2E.EventResponseMessage_EventResponseType) (*waE2E.Message, error) {
	plaintext, err := proto.Marshal(&waE2E.EventResponseMessage{
		Response:    resp.Enum(),
		TimestampMS: proto.Int64(time.Now().UnixMilli()),
	})
	if err != nil {
		return nil, err
	}
	ownID := s.client.Store.GetLID()
	if ev.Sender.Server == types.DefaultUserServer && s.client.Store.ID != nil {
		ownID = *s.client.Store.ID
	}
	baseKey, origSender, err := s.client.Store.MsgSecrets.GetMessageSecret(ctx, ev.Chat, ev.Sender, ev.ID)
	if err != nil {
		return nil, err
	}
	if baseKey == nil {
		return nil, fmt.Errorf("segredo do evento não encontrado (o evento precisa ter sido recebido por esta sessão)")
	}
	secretKey, ad := eventSecretKey(ownID, ev.ID, origSender, baseKey)
	iv := random.Bytes(12)
	ciphertext, err := gcmutil.Encrypt(secretKey, iv, plaintext, ad)
	if err != nil {
		return nil, err
	}
	key := &waCommon.MessageKey{
		RemoteJID: proto.String(ev.Chat.String()),
		FromMe:    proto.Bool(ev.IsFromMe),
		ID:        proto.String(ev.ID),
	}
	if ev.IsGroup {
		key.Participant = proto.String(ev.Sender.String())
	}
	return &waE2E.Message{EncEventResponseMessage: &waE2E.EncEventResponseMessage{
		EventCreationMessageKey: key,
		EncPayload:              ciphertext,
		EncIV:                   iv,
	}}, nil
}

// eventOrigSender espelha getOrigSenderFromKey do whatsmeow.
func eventOrigSender(evt *events.Message, key *waCommon.MessageKey) (types.JID, error) {
	if key.GetFromMe() {
		return evt.Info.Sender, nil
	}
	if evt.Info.Chat.Server == types.DefaultUserServer || evt.Info.Chat.Server == types.HiddenUserServer {
		return types.ParseJID(key.GetRemoteJID())
	}
	return types.ParseJID(key.GetParticipant())
}

// decryptEventResponse decodifica um EncEventResponseMessage recebido.
func (s *Session) decryptEventResponse(ctx context.Context, evt *events.Message) (*waE2E.EventResponseMessage, error) {
	enc := evt.Message.GetEncEventResponseMessage()
	if enc == nil {
		return nil, fmt.Errorf("não é EncEventResponseMessage")
	}
	key := enc.GetEventCreationMessageKey()
	origSender, err := eventOrigSender(evt, key)
	if err != nil {
		return nil, err
	}
	baseKey, storedOrig, err := s.client.Store.MsgSecrets.GetMessageSecret(ctx, evt.Info.Chat, origSender, key.GetID())
	if err != nil {
		return nil, err
	}
	if baseKey == nil {
		return nil, fmt.Errorf("segredo do evento não encontrado")
	}
	secretKey, ad := eventSecretKey(evt.Info.Sender, key.GetID(), origSender, baseKey)
	plaintext, err := gcmutil.Decrypt(secretKey, enc.GetEncIV(), enc.GetEncPayload(), ad)
	if err != nil && origSender.String() != storedOrig.String() && strings.Contains(err.Error(), "authentication failed") {
		secretKey, ad = eventSecretKey(evt.Info.Sender, key.GetID(), storedOrig, baseKey)
		plaintext, err = gcmutil.Decrypt(secretKey, enc.GetEncIV(), enc.GetEncPayload(), ad)
	}
	if err != nil {
		return nil, err
	}
	var erm waE2E.EventResponseMessage
	if err := proto.Unmarshal(plaintext, &erm); err != nil {
		return nil, err
	}
	return &erm, nil
}

// ---- rótulos ----

func eventResponseText(t waE2E.EventResponseMessage_EventResponseType) (emoji, label string) {
	switch t {
	case waE2E.EventResponseMessage_GOING:
		return "✅", "vai"
	case waE2E.EventResponseMessage_NOT_GOING:
		return "❌", "não vai"
	case waE2E.EventResponseMessage_MAYBE:
		return "🤔", "talvez"
	}
	return "❔", "sem resposta"
}

func parseEventResponse(s string) (waE2E.EventResponseMessage_EventResponseType, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "going", "vai", "yes", "sim", "1":
		return waE2E.EventResponseMessage_GOING, true
	case "not_going", "not going", "nao_vai", "não vai", "nao vai", "no", "2":
		return waE2E.EventResponseMessage_NOT_GOING, true
	case "maybe", "talvez", "3":
		return waE2E.EventResponseMessage_MAYBE, true
	}
	return waE2E.EventResponseMessage_UNKNOWN, false
}

// ---- (b) responder um evento ----

// POST /api/sessions/{sid}/messages/event-response
// { "to": "<número|JID>", "eventMessageId": "<ID>", "response": "going|not_going|maybe" }
func (s *server) handleEventResponse(w http.ResponseWriter, r *http.Request) {
	sess := s.pairedSession(w, r.PathValue("sid"))
	if sess == nil {
		return
	}
	var b struct {
		To             string `json:"to"`
		EventMessageID string `json:"eventMessageId"`
		Response       string `json:"response"`
	}
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil || b.EventMessageID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "eventMessageId obrigatório"})
		return
	}
	resp, ok := parseEventResponse(b.Response)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "response deve ser going, not_going ou maybe"})
		return
	}
	chatStr, senderStr, fromMe, _, err := sess.mgr.store.findMessage(r.Context(), sess.id, b.EventMessageID)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "evento não encontrado pelo ID"})
		return
	}
	chat, _ := types.ParseJID(chatStr)
	sender, _ := types.ParseJID(senderStr)
	info := &types.MessageInfo{
		MessageSource: types.MessageSource{Chat: chat, Sender: sender, IsFromMe: fromMe, IsGroup: chat.Server == types.GroupServer},
		ID:            b.EventMessageID,
	}
	msg, err := sess.buildEventResponse(r.Context(), info, resp)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	target := chat
	if b.To != "" {
		if j, e := resolveRecipient(b.To); e == nil {
			target = j
		}
	}
	s.sendTo(sess, w, r, target, msg)
}

// ---- (2) receber RSVPs ----

func (s *Session) handleIncomingEventResponse(evt *events.Message) {
	erm, err := s.decryptEventResponse(s.mgr.appCtx, evt)
	if err != nil {
		s.log.Warn("decrypt event response falhou", "err", err)
		return
	}
	eventID := evt.Message.GetEncEventResponseMessage().GetEventCreationMessageKey().GetID()
	emoji, label := eventResponseText(erm.GetResponse())
	who := evt.Info.PushName
	if who == "" {
		who = s.realPhone(evt.Info.Sender)
	}

	s.dispatchWebhook("event_response", map[string]any{
		"eventMessageId": eventID,
		"guest":          evt.Info.Sender.String(),
		"guestPhone":     s.realPhone(evt.Info.Sender),
		"chat":           evt.Info.Chat.String(),
		"response":       erm.GetResponse().String(), // GOING | NOT_GOING | MAYBE
		"label":          label,
		"timestamp":      evt.Info.Timestamp.UnixMilli(),
	})

	if cfg := s.getChatwoot(); cfg.valid() {
		s.chatwootPushVote(cfg, evt, emoji+" "+who+" "+label+" (evento)")
	}
}

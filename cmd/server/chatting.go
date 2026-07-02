package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"
)

// msgTarget resolve o par (chat, remetente) de uma mensagem existente a partir do
// corpo da requisição. Para ações como reação, edição, exclusão e "visto", o
// WhatsApp precisa saber quem enviou a mensagem original:
//   - chat: o JID da conversa (campo "to")
//   - sender: quem mandou a msg. Em conversa direta é o próprio contato (ou nós,
//     se fromMe). Em grupo, é o "participant".
func (s *server) msgTarget(sess *Session, to, participant string, fromMe bool) (chat, sender types.JID, err error) {
	chat, err = resolveRecipient(to)
	if err != nil {
		return
	}
	switch {
	case strings.TrimSpace(participant) != "":
		sender, err = types.ParseJID(participant)
	case fromMe && sess.client.Store.ID != nil:
		sender = sess.client.Store.ID.ToNonAD()
	default:
		sender = chat
	}
	return
}

// POST /api/sessions/{sid}/messages/location  {to, latitude, longitude, name?, address?}
func (s *server) handleSendLocation(w http.ResponseWriter, r *http.Request) {
	sess := s.pairedSession(w, r.PathValue("sid"))
	if sess == nil {
		return
	}
	var b struct {
		To        string  `json:"to"`
		Latitude  float64 `json:"latitude"`
		Longitude float64 `json:"longitude"`
		Name      string  `json:"name"`
		Address   string  `json:"address"`
	}
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid body"})
		return
	}
	s.send(sess, w, r, b.To, &waE2E.Message{LocationMessage: &waE2E.LocationMessage{
		DegreesLatitude:  proto.Float64(b.Latitude),
		DegreesLongitude: proto.Float64(b.Longitude),
		Name:             proto.String(b.Name),
		Address:          proto.String(b.Address),
	}})
}

// POST /api/sessions/{sid}/messages/contact  {to, contacts:[{fullName, phone, vcard?}]}
func (s *server) handleSendContact(w http.ResponseWriter, r *http.Request) {
	sess := s.pairedSession(w, r.PathValue("sid"))
	if sess == nil {
		return
	}
	var b struct {
		To       string `json:"to"`
		Contacts []struct {
			FullName string `json:"fullName"`
			Phone    string `json:"phone"`
			Vcard    string `json:"vcard"`
		} `json:"contacts"`
	}
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil || len(b.Contacts) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "contacts required"})
		return
	}
	build := func(name, phone, vcard string) *waE2E.ContactMessage {
		if strings.TrimSpace(vcard) == "" {
			vcard = "BEGIN:VCARD\nVERSION:3.0\nFN:" + name +
				"\nTEL;type=CELL;waid=" + normalizePhone(phone) + ":" + phone + "\nEND:VCARD"
		}
		return &waE2E.ContactMessage{DisplayName: proto.String(name), Vcard: proto.String(vcard)}
	}
	// Um contato → ContactMessage; vários → ContactsArrayMessage.
	if len(b.Contacts) == 1 {
		c := b.Contacts[0]
		s.send(sess, w, r, b.To, &waE2E.Message{ContactMessage: build(c.FullName, c.Phone, c.Vcard)})
		return
	}
	arr := make([]*waE2E.ContactMessage, 0, len(b.Contacts))
	for _, c := range b.Contacts {
		arr = append(arr, build(c.FullName, c.Phone, c.Vcard))
	}
	s.send(sess, w, r, b.To, &waE2E.Message{ContactsArrayMessage: &waE2E.ContactsArrayMessage{Contacts: arr}})
}

// POST /api/sessions/{sid}/messages/poll  {to, name, options:[...], multipleAnswers?}
func (s *server) handleSendPoll(w http.ResponseWriter, r *http.Request) {
	sess := s.pairedSession(w, r.PathValue("sid"))
	if sess == nil {
		return
	}
	var b struct {
		To              string   `json:"to"`
		Name            string   `json:"name"`
		Options         []string `json:"options"`
		MultipleAnswers bool     `json:"multipleAnswers"`
	}
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil || strings.TrimSpace(b.Name) == "" || len(b.Options) < 2 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name and at least 2 options required"})
		return
	}
	selectable := 1
	if b.MultipleAnswers {
		selectable = 0 // 0 = múltipla escolha sem limite
	}
	s.send(sess, w, r, b.To, sess.client.BuildPollCreation(b.Name, b.Options, selectable))
}

// POST /api/sessions/{sid}/messages/link-preview  {to, url, text?, title?, description?}
func (s *server) handleSendLinkPreview(w http.ResponseWriter, r *http.Request) {
	sess := s.pairedSession(w, r.PathValue("sid"))
	if sess == nil {
		return
	}
	var b struct {
		To, URL, Text, Title, Description string
	}
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil || strings.TrimSpace(b.URL) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "url required"})
		return
	}
	text := b.Text
	if text == "" {
		text = b.URL
	}
	s.send(sess, w, r, b.To, &waE2E.Message{ExtendedTextMessage: &waE2E.ExtendedTextMessage{
		Text:        proto.String(text),
		MatchedText: proto.String(b.URL),
		Title:       proto.String(b.Title),
		Description: proto.String(b.Description),
	}})
}

// PUT /api/sessions/{sid}/messages/react  {to, messageId, reaction, participant?, fromMe?}
func (s *server) handleReact(w http.ResponseWriter, r *http.Request) {
	sess := s.pairedSession(w, r.PathValue("sid"))
	if sess == nil {
		return
	}
	var b struct {
		To, MessageID, Reaction, Participant string
		FromMe                               bool
	}
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil || b.MessageID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "messageId required"})
		return
	}
	chat, sender, err := s.msgTarget(sess, b.To, b.Participant, b.FromMe)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	msg := sess.client.BuildReaction(chat, sender, b.MessageID, b.Reaction)
	s.sendTo(sess, w, r, chat, msg)
}

// PUT /api/sessions/{sid}/messages/edit  {to, messageId, text}
func (s *server) handleEditMessage(w http.ResponseWriter, r *http.Request) {
	sess := s.pairedSession(w, r.PathValue("sid"))
	if sess == nil {
		return
	}
	var b struct{ To, MessageID, Text string }
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil || b.MessageID == "" || strings.TrimSpace(b.Text) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "messageId and text required"})
		return
	}
	chat, err := resolveRecipient(b.To)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	newMsg := &waE2E.Message{Conversation: proto.String(b.Text)}
	s.sendTo(sess, w, r, chat, sess.client.BuildEdit(chat, b.MessageID, newMsg))
}

// DELETE /api/sessions/{sid}/messages  {to, messageId, participant?, fromMe?}  (revoke)
func (s *server) handleDeleteMessage(w http.ResponseWriter, r *http.Request) {
	sess := s.pairedSession(w, r.PathValue("sid"))
	if sess == nil {
		return
	}
	var b struct {
		To, MessageID, Participant string
		FromMe                     bool
	}
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil || b.MessageID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "messageId required"})
		return
	}
	chat, sender, err := s.msgTarget(sess, b.To, b.Participant, b.FromMe)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	s.sendTo(sess, w, r, chat, sess.client.BuildRevoke(chat, sender, b.MessageID))
}

// POST /api/sessions/{sid}/messages/seen  {to, messageId?, messageIds?, participant?}
func (s *server) handleMarkSeen(w http.ResponseWriter, r *http.Request) {
	sess := s.pairedSession(w, r.PathValue("sid"))
	if sess == nil {
		return
	}
	var b struct {
		To          string   `json:"to"`
		MessageID   string   `json:"messageId"`
		MessageIDs  []string `json:"messageIds"`
		Participant string   `json:"participant"`
	}
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid body"})
		return
	}
	ids := b.MessageIDs
	if b.MessageID != "" {
		ids = append(ids, b.MessageID)
	}
	if len(ids) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "messageId(s) required"})
		return
	}
	chat, sender, err := s.msgTarget(sess, b.To, b.Participant, false)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if err := sess.client.MarkRead(r.Context(), ids, time.Now(), chat, sender); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "ids": ids})
}

// POST /api/sessions/{sid}/messages/typing  {to, typing:true|false, audio?:bool}
func (s *server) handleTyping(w http.ResponseWriter, r *http.Request) {
	sess := s.pairedSession(w, r.PathValue("sid"))
	if sess == nil {
		return
	}
	var b struct {
		To     string `json:"to"`
		Typing bool   `json:"typing"`
		Audio  bool   `json:"audio"`
	}
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid body"})
		return
	}
	chat, err := resolveRecipient(b.To)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	state := types.ChatPresencePaused
	if b.Typing {
		state = types.ChatPresenceComposing
	}
	media := types.ChatPresenceMediaText
	if b.Audio {
		media = types.ChatPresenceMediaAudio
	}
	if err := sess.client.SendChatPresence(r.Context(), chat, state, media); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// sendTo despacha uma mensagem já montada para um JID resolvido e devolve o
// mesmo formato de resposta de send().
func (s *server) sendTo(sess *Session, w http.ResponseWriter, r *http.Request, jid types.JID, msg *waE2E.Message) {
	resp, err := sess.client.SendMessage(r.Context(), jid, msg)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	sess.recordOutgoing(jid, resp.ID, resp.Timestamp.UnixMilli(), msg)
	writeJSON(w, http.StatusOK, map[string]any{
		"id": resp.ID, "to": jid.String(), "timestamp": resp.Timestamp.UnixMilli(),
	})
}

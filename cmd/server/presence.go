package main

import (
	"encoding/json"
	"net/http"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"
)

// ---- Presença ----

// POST /api/sessions/{sid}/presence  {available:true|false}
// Presença global da conta (online/offline).
func (s *server) handleSetPresence(w http.ResponseWriter, r *http.Request) {
	sess := s.pairedSession(w, r.PathValue("sid"))
	if sess == nil {
		return
	}
	var b struct {
		Available bool `json:"available"`
	}
	_ = json.NewDecoder(r.Body).Decode(&b)
	state := types.PresenceUnavailable
	if b.Available {
		state = types.PresenceAvailable
	}
	if err := sess.client.SendPresence(r.Context(), state); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// POST /api/sessions/{sid}/presence/{jid}/subscribe
// Assina os eventos de presença (online/última vez visto) de um contato.
func (s *server) handleSubscribePresence(w http.ResponseWriter, r *http.Request) {
	sess := s.pairedSession(w, r.PathValue("sid"))
	if sess == nil {
		return
	}
	jid, err := resolveRecipient(r.PathValue("jid"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if err := sess.client.SubscribePresence(r.Context(), jid); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ---- Perfil (próprio) ----

// GET /api/sessions/{sid}/profile  → dados da conta autenticada
func (s *server) handleGetProfile(w http.ResponseWriter, r *http.Request) {
	sess := s.pairedSession(w, r.PathValue("sid"))
	if sess == nil {
		return
	}
	id := sess.client.Store.ID
	out := map[string]any{
		"jid":      id.String(),
		"number":   id.User,
		"pushName": sess.client.Store.PushName,
	}
	writeJSON(w, http.StatusOK, out)
}

// PUT /api/sessions/{sid}/profile/status  {status}  → recado/About
func (s *server) handleSetProfileStatus(w http.ResponseWriter, r *http.Request) {
	sess := s.pairedSession(w, r.PathValue("sid"))
	if sess == nil {
		return
	}
	var b struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "status required"})
		return
	}
	if err := sess.client.SetStatusMessage(r.Context(), b.Status); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ---- Status / Stories ----
// Postagens vão para status@broadcast; o whatsmeow resolve os destinatários
// (seus contatos) automaticamente.

// POST /api/sessions/{sid}/status/text  {text}
func (s *server) handleStatusText(w http.ResponseWriter, r *http.Request) {
	sess := s.pairedSession(w, r.PathValue("sid"))
	if sess == nil {
		return
	}
	var b struct {
		Text string `json:"text"`
	}
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil || b.Text == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "text required"})
		return
	}
	s.sendTo(sess, w, r, types.StatusBroadcastJID, &waE2E.Message{
		ExtendedTextMessage: &waE2E.ExtendedTextMessage{Text: proto.String(b.Text)},
	})
}

// POST /api/sessions/{sid}/status/image  {base64|url, caption?}
func (s *server) handleStatusImage(w http.ResponseWriter, r *http.Request) {
	sess := s.pairedSession(w, r.PathValue("sid"))
	if sess == nil {
		return
	}
	var b struct{ Base64, URL, Caption, Mimetype string }
	_ = json.NewDecoder(r.Body).Decode(&b)
	up, ok := s.uploadMedia(sess, w, r, b.Base64, b.URL, whatsmeow.MediaImage)
	if !ok {
		return
	}
	mime := b.Mimetype
	if mime == "" {
		mime = "image/jpeg"
	}
	s.sendTo(sess, w, r, types.StatusBroadcastJID, &waE2E.Message{ImageMessage: &waE2E.ImageMessage{
		Caption: proto.String(b.Caption), Mimetype: proto.String(mime),
		URL: &up.URL, DirectPath: &up.DirectPath, MediaKey: up.MediaKey,
		FileEncSHA256: up.FileEncSHA256, FileSHA256: up.FileSHA256, FileLength: proto.Uint64(up.FileLength),
	}})
}

// POST /api/sessions/{sid}/status/video  {base64|url, caption?}
func (s *server) handleStatusVideo(w http.ResponseWriter, r *http.Request) {
	sess := s.pairedSession(w, r.PathValue("sid"))
	if sess == nil {
		return
	}
	var b struct{ Base64, URL, Caption, Mimetype string }
	_ = json.NewDecoder(r.Body).Decode(&b)
	up, ok := s.uploadMedia(sess, w, r, b.Base64, b.URL, whatsmeow.MediaVideo)
	if !ok {
		return
	}
	mime := b.Mimetype
	if mime == "" {
		mime = "video/mp4"
	}
	s.sendTo(sess, w, r, types.StatusBroadcastJID, &waE2E.Message{VideoMessage: &waE2E.VideoMessage{
		Caption: proto.String(b.Caption), Mimetype: proto.String(mime),
		URL: &up.URL, DirectPath: &up.DirectPath, MediaKey: up.MediaKey,
		FileEncSHA256: up.FileEncSHA256, FileSHA256: up.FileSHA256, FileLength: proto.Uint64(up.FileLength),
	}})
}

// POST /api/sessions/{sid}/status/audio  {base64|url}
func (s *server) handleStatusAudio(w http.ResponseWriter, r *http.Request) {
	sess := s.pairedSession(w, r.PathValue("sid"))
	if sess == nil {
		return
	}
	var b struct{ Base64, URL, Mimetype string }
	_ = json.NewDecoder(r.Body).Decode(&b)
	up, ok := s.uploadMedia(sess, w, r, b.Base64, b.URL, whatsmeow.MediaAudio)
	if !ok {
		return
	}
	mime := b.Mimetype
	if mime == "" {
		mime = "audio/ogg; codecs=opus"
	}
	s.sendTo(sess, w, r, types.StatusBroadcastJID, &waE2E.Message{AudioMessage: &waE2E.AudioMessage{
		Mimetype: proto.String(mime), PTT: proto.Bool(true),
		URL: &up.URL, DirectPath: &up.DirectPath, MediaKey: up.MediaKey,
		FileEncSHA256: up.FileEncSHA256, FileSHA256: up.FileSHA256, FileLength: proto.Uint64(up.FileLength),
	}})
}

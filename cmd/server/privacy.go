package main

import (
	"encoding/json"
	"net/http"
	"time"

	"go.mau.fi/whatsmeow/types"
)

// GET /api/sessions/{sid}/privacy  → configurações de privacidade da conta
func (s *server) handleGetPrivacy(w http.ResponseWriter, r *http.Request) {
	sess := s.pairedSession(w, r.PathValue("sid"))
	if sess == nil {
		return
	}
	p := sess.client.GetPrivacySettings(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{
		"groupAdd":     string(p.GroupAdd),
		"lastSeen":     string(p.LastSeen),
		"status":       string(p.Status),
		"profile":      string(p.Profile),
		"readReceipts": string(p.ReadReceipts),
		"callAdd":      string(p.CallAdd),
		"online":       string(p.Online),
		"messages":     string(p.Messages),
	})
}

// PUT /api/sessions/{sid}/privacy  {name, value}
// name: groupadd|last|status|profile|readreceipts|online|calladd|messages
// value: all|contacts|contact_blacklist|none|known|match_last_seen
func (s *server) handleSetPrivacy(w http.ResponseWriter, r *http.Request) {
	sess := s.pairedSession(w, r.PathValue("sid"))
	if sess == nil {
		return
	}
	var b struct {
		Name  string `json:"name"`
		Value string `json:"value"`
	}
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil || b.Name == "" || b.Value == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name and value required"})
		return
	}
	p, err := sess.client.SetPrivacySetting(r.Context(), types.PrivacySettingType(b.Name), types.PrivacySetting(b.Value))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "settings": p})
}

// GET /api/sessions/{sid}/privacy/status  → quem pode ver cada status (lista)
func (s *server) handleGetStatusPrivacy(w http.ResponseWriter, r *http.Request) {
	sess := s.pairedSession(w, r.PathValue("sid"))
	if sess == nil {
		return
	}
	sp, err := sess.client.GetStatusPrivacy(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"statusPrivacy": sp})
}

// PUT /api/sessions/{sid}/disappearing  {seconds}
// mensagens temporárias PADRÃO p/ novas conversas. 0 = desliga.
// Valores aceitos pelo WhatsApp: 0, 86400 (24h), 604800 (7d), 7776000 (90d).
func (s *server) handleSetDefaultDisappearing(w http.ResponseWriter, r *http.Request) {
	sess := s.pairedSession(w, r.PathValue("sid"))
	if sess == nil {
		return
	}
	var b struct {
		Seconds int64 `json:"seconds"`
	}
	_ = json.NewDecoder(r.Body).Decode(&b)
	if err := sess.client.SetDefaultDisappearingTimer(r.Context(), time.Duration(b.Seconds)*time.Second); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "seconds": b.Seconds})
}

// PUT /api/sessions/{sid}/chats/{chatId}/disappearing  {seconds}
// mensagens temporárias para UMA conversa específica. 0 = desliga.
func (s *server) handleSetChatDisappearing(w http.ResponseWriter, r *http.Request) {
	sess := s.pairedSession(w, r.PathValue("sid"))
	if sess == nil {
		return
	}
	chat, err := resolveRecipient(r.PathValue("chatId"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	var b struct {
		Seconds int64 `json:"seconds"`
	}
	_ = json.NewDecoder(r.Body).Decode(&b)
	if err := sess.client.SetDisappearingTimer(r.Context(), chat, time.Duration(b.Seconds)*time.Second, time.Now()); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "chat": chat.String(), "seconds": b.Seconds})
}

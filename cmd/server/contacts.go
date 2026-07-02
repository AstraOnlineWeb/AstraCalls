package main

import (
	"net/http"
	"strings"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

// GET /api/sessions/{sid}/contacts/check?phone=...
// Consulta autoritativa ao WhatsApp: o número existe? Qual o JID canônico?
// Resolve o problema do 9º dígito (BR) — devolve o JID real independente de o
// número ter sido digitado com ou sem o 9.
func (s *server) handleCheckNumber(w http.ResponseWriter, r *http.Request) {
	sess := s.pairedSession(w, r.PathValue("sid"))
	if sess == nil {
		return
	}
	phone := strings.TrimSpace(r.URL.Query().Get("phone"))
	if phone == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "phone required"})
		return
	}
	if !strings.HasPrefix(phone, "+") {
		phone = "+" + normalizePhone(phone)
	}
	resp, err := sess.client.IsOnWhatsApp(r.Context(), []string{phone})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if len(resp) == 0 {
		writeJSON(w, http.StatusOK, map[string]any{"query": phone, "exists": false})
		return
	}
	it := resp[0]
	out := map[string]any{"query": it.Query, "exists": it.IsIn}
	if it.IsIn {
		out["jid"] = it.JID.String()
		out["number"] = it.JID.User
	}
	if it.VerifiedName != nil && it.VerifiedName.Details != nil {
		out["verifiedName"] = it.VerifiedName.Details.GetVerifiedName()
	}
	writeJSON(w, http.StatusOK, out)
}

// GET /api/sessions/{sid}/contacts  → todos os contatos conhecidos
func (s *server) handleListContacts(w http.ResponseWriter, r *http.Request) {
	sess := s.pairedSession(w, r.PathValue("sid"))
	if sess == nil {
		return
	}
	all, err := sess.client.Store.Contacts.GetAllContacts(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	out := make([]map[string]any, 0, len(all))
	for jid, c := range all {
		out = append(out, map[string]any{
			"jid":          jid.String(),
			"number":       jid.User,
			"fullName":     c.FullName,
			"firstName":    c.FirstName,
			"pushName":     c.PushName,
			"businessName": c.BusinessName,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// GET /api/sessions/{sid}/contacts/{jid}  → info (status/about, foto, devices)
func (s *server) handleContactInfo(w http.ResponseWriter, r *http.Request) {
	sess := s.pairedSession(w, r.PathValue("sid"))
	if sess == nil {
		return
	}
	jid, err := resolveRecipient(r.PathValue("jid"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	info, err := sess.client.GetUserInfo(r.Context(), []types.JID{jid})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	ui, ok := info[jid]
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	out := map[string]any{
		"jid":       jid.String(),
		"number":    jid.User,
		"status":    ui.Status,
		"pictureId": ui.PictureID,
	}
	if !ui.LID.IsEmpty() {
		out["lid"] = ui.LID.String()
	}
	writeJSON(w, http.StatusOK, out)
}

// GET /api/sessions/{sid}/contacts/{jid}/picture?preview=true  → URL da foto
func (s *server) handleContactPicture(w http.ResponseWriter, r *http.Request) {
	sess := s.pairedSession(w, r.PathValue("sid"))
	if sess == nil {
		return
	}
	jid, err := resolveRecipient(r.PathValue("jid"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	pic, err := sess.client.GetProfilePictureInfo(r.Context(), jid, &whatsmeow.GetProfilePictureParams{
		Preview: r.URL.Query().Get("preview") == "true",
	})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if pic == nil {
		writeJSON(w, http.StatusOK, map[string]any{"url": ""})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"url": pic.URL, "id": pic.ID, "type": pic.Type})
}

// POST /api/sessions/{sid}/contacts/{jid}/block  e /unblock
func (s *server) handleBlock(block bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sess := s.pairedSession(w, r.PathValue("sid"))
		if sess == nil {
			return
		}
		jid, err := resolveRecipient(r.PathValue("jid"))
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		action := events.BlocklistChangeActionUnblock
		if block {
			action = events.BlocklistChangeActionBlock
		}
		if _, err := sess.client.UpdateBlocklist(r.Context(), jid, action); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "blocked": block})
	}
}

// GET /api/sessions/{sid}/blocklist  → JIDs bloqueados
func (s *server) handleBlocklist(w http.ResponseWriter, r *http.Request) {
	sess := s.pairedSession(w, r.PathValue("sid"))
	if sess == nil {
		return
	}
	bl, err := sess.client.GetBlocklist(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	jids := make([]string, 0, len(bl.JIDs))
	for _, j := range bl.JIDs {
		jids = append(jids, j.String())
	}
	writeJSON(w, http.StatusOK, map[string]any{"blocked": jids})
}

package main

import (
	"net/http"
)

// GET /api/sessions/{sid}/contacts/{jid}/business  → perfil comercial (business)
func (s *server) handleBusinessProfile(w http.ResponseWriter, r *http.Request) {
	sess := s.pairedSession(w, r.PathValue("sid"))
	if sess == nil {
		return
	}
	jid, err := resolveRecipient(r.PathValue("jid"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	bp, err := sess.client.GetBusinessProfile(r.Context(), jid)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if bp == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not a business account"})
		return
	}
	cats := make([]map[string]any, 0, len(bp.Categories))
	for _, c := range bp.Categories {
		cats = append(cats, map[string]any{"id": c.ID, "name": c.Name})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"jid":           bp.JID.String(),
		"address":       bp.Address,
		"email":         bp.Email,
		"categories":    cats,
		"timezone":      bp.BusinessHoursTimeZone,
		"businessHours": bp.BusinessHours,
	})
}

// GET /api/sessions/{sid}/profile/qr?revoke=  → link/QR "me adicione" da conta
func (s *server) handleContactQR(w http.ResponseWriter, r *http.Request) {
	sess := s.pairedSession(w, r.PathValue("sid"))
	if sess == nil {
		return
	}
	revoke := r.URL.Query().Get("revoke") == "true"
	link, err := sess.client.GetContactQRLink(r.Context(), revoke)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"link": link})
}

package main

import (
	"encoding/json"
	"net/http"
)

// Endpoints HTTP para status e configuração SIP por sessão.

func (s *server) handleSIPStatus(w http.ResponseWriter, r *http.Request) {
	if s.sipGW == nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"enabled": false,
			"message": "SIP Gateway not started",
		})
		return
	}

	s.sipGW.mu.RLock()
	regs := make([]map[string]string, 0, len(s.sipGW.registrations))
	for _, reg := range s.sipGW.registrations {
		regs = append(regs, map[string]string{
			"sip_user":   reg.sipUser,
			"session_id": reg.sessionID,
			"contact":    reg.contact,
		})
	}
	calls := make([]map[string]string, 0, len(s.sipGW.activeCalls))
	for _, sc := range s.sipGW.activeCalls {
		calls = append(calls, map[string]string{
			"sip_call_id": sc.sipCallID,
			"session_id":  sc.sessionID,
			"wa_call_id":  sc.waCallID,
			"from":        sc.fromURI,
			"to":          sc.toURI,
		})
	}
	s.sipGW.mu.RUnlock()

	writeJSON(w, http.StatusOK, map[string]any{
		"enabled":       true,
		"port":          5060,
		"registrations": regs,
		"active_calls":  calls,
	})
}

func (s *server) handleSIPConfig(w http.ResponseWriter, r *http.Request) {
	sid := r.PathValue("sid")
	sess := s.sessionByID(w, sid)
	if sess == nil {
		return
	}

	if r.Method == http.MethodGet {
		writeJSON(w, http.StatusOK, map[string]any{
			"sip_user":   sess.SIPUser,
			"sip_pass":   sess.SIPPass,
			"sip_url":    sess.SIPURL,
			"sip_realm":  "astracalls",
			"sip_port":   5060,
			"codecs":     []string{"PCMU/8000"},
			"session_id": sess.id,
		})
		return
	}

	// POST: atualizar config SIP
	var body struct {
		SIPUser string `json:"sip_user"`
		SIPPass string `json:"sip_pass"`
		SIPURL  string `json:"sip_url"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid body"})
		return
	}
	if body.SIPUser != "" {
		sess.SIPUser = body.SIPUser
	}
	if body.SIPPass != "" {
		sess.SIPPass = body.SIPPass
	}
	if body.SIPURL != "" {
		sess.SIPURL = body.SIPURL
	}
	_ = s.sessions.store.setSIP(r.Context(), sess.id, sess.SIPUser, sess.SIPPass)
	writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}

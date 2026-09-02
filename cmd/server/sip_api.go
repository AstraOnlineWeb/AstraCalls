package main

import (
	"encoding/json"
	"net/http"
	"strconv"
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

// handleSIPExtConfig lê/grava a config do MODELO 2: registrar esta sessão num PBX
// externo (a sessão vira um ramal registrado no Asterisk/FreePBX do cliente).
func (s *server) handleSIPExtConfig(w http.ResponseWriter, r *http.Request) {
	sess := s.sessionByID(w, r.PathValue("sid"))
	if sess == nil {
		return
	}

	if r.Method == http.MethodGet {
		cfg := sess.sipExtSnapshot()
		state, lastErr := "", ""
		if s.sipGW != nil {
			state, lastErr = s.sipGW.extRegStatus(sess.id)
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"enabled":   cfg.Enabled,
			"host":      cfg.Host,
			"port":      cfg.Port,
			"user":      cfg.User,
			"pass":      cfg.Pass,
			"dest":      cfg.Dest,
			"status":    state,
			"error":     lastErr,
			"advertise": sipAdvertiseHost() + ":" + itoa(sipAdvertisePort()),
		})
		return
	}

	// POST: atualiza e aplica.
	var body struct {
		Enabled bool   `json:"enabled"`
		Host    string `json:"host"`
		Port    int    `json:"port"`
		User    string `json:"user"`
		Pass    string `json:"pass"`
		Dest    string `json:"dest"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid body"})
		return
	}
	if body.Port <= 0 {
		body.Port = 5060
	}
	if body.Enabled && (body.Host == "" || body.User == "") {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "host e usuário são obrigatórios"})
		return
	}
	cfg := sipExtConfig{
		Enabled: body.Enabled, Host: body.Host, Port: body.Port,
		User: body.User, Pass: body.Pass, Dest: body.Dest,
	}
	sess.setSIPExt(cfg)
	if err := s.sessions.store.setSIPExt(r.Context(), sess.id, cfg.Enabled, cfg.Host, cfg.Port, cfg.User, cfg.Pass, cfg.Dest); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if s.sipGW != nil {
		s.sipGW.applyExtRegistration(sess)
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}

func itoa(n int) string { return strconv.Itoa(n) }

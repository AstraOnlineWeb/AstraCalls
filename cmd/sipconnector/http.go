package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
)

func (s *server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	})

	// Só chave-mestra (o Botfy, servidor): configuração e tokens.
	mux.HandleFunc("GET /api/me", s.requireAdmin(s.handleMe))
	mux.HandleFunc("POST /api/widget-tokens", s.requireAdmin(s.handleWidgetToken))
	mux.HandleFunc("GET /api/lines", s.requireAdmin(s.handleLineList))
	mux.HandleFunc("GET /api/lines/{id}", s.requireAdmin(s.handleLineGet))
	mux.HandleFunc("PUT /api/lines/{id}", s.requireAdmin(s.handleLinePut))
	mux.HandleFunc("DELETE /api/lines/{id}", s.requireAdmin(s.handleLineDelete))
	mux.HandleFunc("GET /api/lines/{id}/status", s.requireAdmin(s.handleLineStatus))
	mux.HandleFunc("GET /api/sessions", s.requireAdmin(s.handleSessions))

	// Chave-mestra OU token de widget da conta: eventos e operações de chamada,
	// no mesmo caminho do wacalls (/api/sessions/{sid}/calls/...).
	mux.HandleFunc("GET /api/events", s.requireAuth(s.handleEvents))
	mux.HandleFunc("POST /api/sessions/{sid}/calls", s.requireAuth(s.handleDial))
	mux.HandleFunc("POST /api/sessions/{sid}/calls/{id}/accept", s.requireAuth(s.handleAccept))
	mux.HandleFunc("POST /api/sessions/{sid}/calls/{id}/reject", s.requireAuth(s.handleReject))
	mux.HandleFunc("DELETE /api/sessions/{sid}/calls/{id}", s.requireAuth(s.handleHangup))
	mux.HandleFunc("POST /api/sessions/{sid}/calls/{id}/webrtc", s.requireAuth(s.handleWebRTC))
	mux.HandleFunc("GET /api/sessions/{sid}/calls/{id}/ws", s.requireAuth(s.handleWS))
	mux.HandleFunc("POST /api/sessions/{sid}/calls/{id}/transfer", s.requireAuth(s.handleTransfer))
	mux.HandleFunc("POST /api/sessions/{sid}/calls/{id}/pickup", s.requireAuth(s.handlePickup))
	return withCORS(mux)
}

// GET /api/me — o Botfy valida a chave ao salvar a integração (como o BCVOZ).
func (s *server) handleMe(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "service": "sipconnector", "version": version,
		"sip": map[string]any{"host": s.cfg.PublicIP, "port": s.cfg.SIPAdvertisePort, "realm": digestRealm},
	})
}

// ---- linhas ----

func (s *server) handleLineList(w http.ResponseWriter, _ *http.Request) {
	out := []map[string]any{}
	for _, l := range s.store.all() {
		out = append(out, l.public())
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *server) handleLineGet(w http.ResponseWriter, r *http.Request) {
	l, ok := s.store.get(r.PathValue("id"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "linha não encontrada"})
		return
	}
	writeJSON(w, http.StatusOK, l.public())
}

func (s *server) handleLinePut(w http.ResponseWriter, r *http.Request) {
	var l Line
	if err := json.NewDecoder(r.Body).Decode(&l); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "corpo inválido"})
		return
	}
	l.ID = r.PathValue("id")
	var prev *Line
	if old, ok := s.store.get(l.ID); ok {
		prev = &old
	}
	if err := l.normalize(prev); err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": err.Error()})
		return
	}
	if err := s.store.put(l); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	s.sip.applyLine(l)
	s.log.Info("linha salva", "line", l.ID, "account", l.AccountID, "mode", l.Mode, "agents", len(l.Agents))
	writeJSON(w, http.StatusOK, l.public())
}

func (s *server) handleLineDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.sip.removeLine(id, true)
	if err := s.store.delete(id); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) handleLineStatus(w http.ResponseWriter, r *http.Request) {
	l, ok := s.store.get(r.PathValue("id"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "linha não encontrada"})
		return
	}
	line, agents := s.sip.lineStatus(l)
	writeJSON(w, http.StatusOK, map[string]any{
		"line": line, "agents": agents, "activeCalls": s.calls.countForLine(l.ID),
		"sip": map[string]any{"host": s.cfg.PublicIP, "port": s.cfg.SIPAdvertisePort, "realm": digestRealm},
	})
}

// GET /api/sessions — mesmo formato do wacalls, para o provider_state do Botfy.
func (s *server) handleSessions(w http.ResponseWriter, _ *http.Request) {
	out := []map[string]any{}
	for _, l := range s.store.all() {
		line, _ := s.sip.lineStatus(l)
		state := "disconnected"
		if line.State == regRegistered {
			state = "connected"
		}
		out = append(out, map[string]any{
			"id": l.ID, "name": l.Name, "state": state, "accountId": l.AccountID, "inboxId": l.InboxID,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// ---- chamadas ----

// POST /api/sessions/{sid}/calls {phone, agentEmail?} -> {call:{callId}}
func (s *server) handleDial(w http.ResponseWriter, r *http.Request, p principal) {
	line, ok := s.store.get(r.PathValue("sid"))
	if !ok || !line.Enabled || !p.canSee(line.AccountID) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such session"})
		return
	}
	var body struct {
		Phone      string `json:"phone"`
		AgentEmail string `json:"agentEmail"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	phone := dialable(body.Phone)
	if phone == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "phone required"})
		return
	}
	c, err := s.sip.dial(line, line.agentByEmail(body.AgentEmail), phone, clientID(r))
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"call": map[string]string{"callId": c.ID}})
}

// dialable mantém dígitos, * e # (o PABX decide a rota; prefixo de saída é dele).
func dialable(v string) string {
	var b strings.Builder
	for _, r := range v {
		if (r >= '0' && r <= '9') || r == '*' || r == '#' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func (s *server) handleAccept(w http.ResponseWriter, r *http.Request, p principal) {
	c := s.callFor(w, r, p)
	if c == nil {
		return
	}
	if err := c.accept(clientID(r)); err != nil {
		writeJSON(w, statusFor(err), map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *server) handleReject(w http.ResponseWriter, r *http.Request, p principal) {
	c := s.callFor(w, r, p)
	if c == nil {
		return
	}
	if err := c.reject(); err != nil {
		writeJSON(w, statusFor(err), map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *server) handleHangup(w http.ResponseWriter, r *http.Request, p principal) {
	c := s.callFor(w, r, p)
	if c == nil {
		return
	}
	c.hangup("user_ended")
	w.WriteHeader(http.StatusNoContent)
}

// POST .../webrtc {sdp_offer} -> {sdp_answer}: o navegador assume o áudio.
func (s *server) handleWebRTC(w http.ResponseWriter, r *http.Request, p principal) {
	c := s.callFor(w, r, p)
	if c == nil {
		return
	}
	var body struct {
		SDPOffer string `json:"sdp_offer"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.SDPOffer == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "sdp_offer required"})
		return
	}
	leg, answer, err := newWebRTCLeg(getWebRTCAPI(s.cfg, s.log), body.SDPOffer, c.fromBrowser, func() {
		c.consumerGone("webrtc-closed")
	})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	c.attachWebRTC(leg)
	writeJSON(w, http.StatusOK, map[string]string{"sdp_answer": answer})
}

// POST .../transfer
//
//	{"target":"sip","extension":"201"} -> REFER para o ramal (o PABX assume a chamada)
//	{"target":"agent"}                 -> libera a chamada para outro navegador pegar
func (s *server) handleTransfer(w http.ResponseWriter, r *http.Request, p principal) {
	c := s.callFor(w, r, p)
	if c == nil {
		return
	}
	var body struct {
		Target    string `json:"target"`
		Extension string `json:"extension"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	switch body.Target {
	case "sip":
		ext := dialable(body.Extension)
		if ext == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "extension required"})
			return
		}
		if err := s.sip.transfer(c, ext); err != nil {
			writeJSON(w, statusFor(err), map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]string{"status": "transferring"})
	case "agent":
		c.mu.Lock()
		c.owner = nil
		c.mu.Unlock()
		s.broker.emit(c.AccountID, map[string]any{
			"type": "call-transfer-offer", "sessionId": c.LineID, "id": c.ID, "peer": c.Peer,
			"from": clientID(r),
		})
		c.publish()
		writeJSON(w, http.StatusOK, map[string]string{"status": "offered"})
	default:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "target deve ser sip ou agent"})
	}
}

// POST .../pickup: o navegador que vai assumir vira o dono.
func (s *server) handlePickup(w http.ResponseWriter, r *http.Request, p principal) {
	c := s.callFor(w, r, p)
	if c == nil {
		return
	}
	owner := clientID(r)
	c.mu.Lock()
	c.owner = &owner
	c.mu.Unlock()
	s.broker.emit(c.AccountID, map[string]any{
		"type": "incoming-claimed", "sessionId": c.LineID, "id": c.ID, "owner": owner,
	})
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func statusFor(err error) int {
	if errors.Is(err, errNotRinging) || errors.Is(err, errNotActive) {
		return http.StatusConflict
	}
	return http.StatusBadGateway
}

package main

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"time"

	"wacalls/internal/voip/core"
	"wacalls/internal/voip/media"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"
)

func (s *server) routes() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/config", s.handleConfig)
	mux.HandleFunc("GET /api/sessions", s.handleSessionList)
	mux.HandleFunc("POST /api/sessions", s.handleSessionCreate)
	mux.HandleFunc("GET /api/sessions/{sid}/calls", s.handleSessionCalls)
	mux.HandleFunc("DELETE /api/sessions/{sid}", s.handleSessionDelete)
	mux.HandleFunc("POST /api/sessions/{sid}/logout", s.handleSessionLogout)
	mux.HandleFunc("POST /api/sessions/{sid}/pair", s.handleSessionPair)
	mux.HandleFunc("POST /api/sessions/{sid}/calls", s.handleStartCall)
	mux.HandleFunc("POST /api/sessions/{sid}/calls/{id}/webrtc", s.handleWebRTC)
	mux.HandleFunc("POST /api/sessions/{sid}/calls/{id}/accept", s.handleAccept)
	mux.HandleFunc("POST /api/sessions/{sid}/calls/{id}/reject", s.handleReject)
	mux.HandleFunc("DELETE /api/sessions/{sid}/calls/{id}", s.handleEndCall)
	mux.HandleFunc("GET /api/sessions/{sid}/history", s.handleHistory)

	// Mensageria (whatsmeow)
	mux.HandleFunc("POST /api/sessions/{sid}/messages/text", s.handleSendText)
	mux.HandleFunc("POST /api/sessions/{sid}/messages/image", s.handleSendImage)
	mux.HandleFunc("POST /api/sessions/{sid}/messages/audio", s.handleSendAudio)
	mux.HandleFunc("POST /api/sessions/{sid}/messages/video", s.handleSendVideo)
	mux.HandleFunc("POST /api/sessions/{sid}/messages/document", s.handleSendDocument)
	mux.HandleFunc("POST /api/sessions/{sid}/messages/location", s.handleSendLocation)
	mux.HandleFunc("POST /api/sessions/{sid}/messages/contact", s.handleSendContact)
	mux.HandleFunc("POST /api/sessions/{sid}/messages/poll", s.handleSendPoll)
	mux.HandleFunc("POST /api/sessions/{sid}/messages/link-preview", s.handleSendLinkPreview)
	mux.HandleFunc("PUT /api/sessions/{sid}/messages/react", s.handleReact)
	mux.HandleFunc("PUT /api/sessions/{sid}/messages/edit", s.handleEditMessage)
	mux.HandleFunc("DELETE /api/sessions/{sid}/messages", s.handleDeleteMessage)
	mux.HandleFunc("POST /api/sessions/{sid}/messages/seen", s.handleMarkSeen)
	mux.HandleFunc("POST /api/sessions/{sid}/messages/typing", s.handleTyping)

	// Contatos (whatsmeow)
	mux.HandleFunc("GET /api/sessions/{sid}/contacts/check", s.handleCheckNumber)
	mux.HandleFunc("GET /api/sessions/{sid}/contacts", s.handleListContacts)
	mux.HandleFunc("GET /api/sessions/{sid}/contacts/{jid}", s.handleContactInfo)
	mux.HandleFunc("GET /api/sessions/{sid}/contacts/{jid}/picture", s.handleContactPicture)
	mux.HandleFunc("POST /api/sessions/{sid}/contacts/{jid}/block", s.handleBlock(true))
	mux.HandleFunc("POST /api/sessions/{sid}/contacts/{jid}/unblock", s.handleBlock(false))
	mux.HandleFunc("GET /api/sessions/{sid}/blocklist", s.handleBlocklist)

	// Grupos (whatsmeow)
	mux.HandleFunc("POST /api/sessions/{sid}/groups", s.handleCreateGroup)
	mux.HandleFunc("GET /api/sessions/{sid}/groups", s.handleListGroups)
	mux.HandleFunc("POST /api/sessions/{sid}/groups/join", s.handleJoinGroup)
	mux.HandleFunc("GET /api/sessions/{sid}/groups/join-info", s.handleGroupJoinInfo)
	mux.HandleFunc("GET /api/sessions/{sid}/groups/{gid}", s.handleGroupInfo)
	mux.HandleFunc("POST /api/sessions/{sid}/groups/{gid}/leave", s.handleLeaveGroup)
	mux.HandleFunc("GET /api/sessions/{sid}/groups/{gid}/participants", s.handleGroupParticipants)
	mux.HandleFunc("POST /api/sessions/{sid}/groups/{gid}/participants/add", s.handleGroupParticipantChange(whatsmeow.ParticipantChangeAdd))
	mux.HandleFunc("POST /api/sessions/{sid}/groups/{gid}/participants/remove", s.handleGroupParticipantChange(whatsmeow.ParticipantChangeRemove))
	mux.HandleFunc("POST /api/sessions/{sid}/groups/{gid}/participants/promote", s.handleGroupParticipantChange(whatsmeow.ParticipantChangePromote))
	mux.HandleFunc("POST /api/sessions/{sid}/groups/{gid}/participants/demote", s.handleGroupParticipantChange(whatsmeow.ParticipantChangeDemote))
	mux.HandleFunc("PUT /api/sessions/{sid}/groups/{gid}/subject", s.handleGroupSubject)
	mux.HandleFunc("PUT /api/sessions/{sid}/groups/{gid}/description", s.handleGroupDescription)
	mux.HandleFunc("PUT /api/sessions/{sid}/groups/{gid}/picture", s.handleGroupPicture)
	mux.HandleFunc("GET /api/sessions/{sid}/groups/{gid}/invite", s.handleGroupInvite)
	mux.HandleFunc("POST /api/sessions/{sid}/groups/{gid}/invite/revoke", s.handleGroupInviteRevoke)
	mux.HandleFunc("PUT /api/sessions/{sid}/groups/{gid}/settings/announce", s.handleGroupAnnounce)
	mux.HandleFunc("PUT /api/sessions/{sid}/groups/{gid}/settings/locked", s.handleGroupLocked)

	// Webhook por sessão (recebimento -> Chatwoot etc.)
	mux.HandleFunc("POST /api/sessions/{sid}/webhook", s.handleSetWebhook)
	mux.HandleFunc("GET /api/sessions/{sid}/webhook", s.handleGetWebhook)
	mux.HandleFunc("DELETE /api/sessions/{sid}/webhook", s.handleDeleteWebhook)

	// Integração Chatwoot por sessão
	mux.HandleFunc("POST /api/sessions/{sid}/chatwoot", s.handleSetChatwoot)
	mux.HandleFunc("GET /api/sessions/{sid}/chatwoot", s.handleGetChatwoot)
	mux.HandleFunc("DELETE /api/sessions/{sid}/chatwoot", s.handleDeleteChatwoot)
	mux.HandleFunc("POST /api/sessions/{sid}/chatwoot/webhook", s.handleChatwootWebhook)
	mux.HandleFunc("GET /api/chatwoot/resolve", s.handleChatwootResolve)

	mux.HandleFunc("GET /api/events", s.handleEvents)

	if s.staticDir != "" {
		if _, err := os.Stat(s.staticDir); err == nil {
			mux.Handle("/", http.FileServer(http.Dir(s.staticDir)))
		}
	}
	var handler http.Handler = mux
	if key := os.Getenv("WACALLS_API_KEY"); key != "" {
		handler = withAuth(handler, key)
	}
	return withCORS(handler)
}

func withCORS(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-Client-Id, X-API-Key")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		h.ServeHTTP(w, r)
	})
}

// withAuth protege as rotas /api/* com uma API key (header X-API-Key ou ?apiKey=).
// Exceções: o webhook do Chatwoot (chamado externamente pelo próprio Chatwoot)
// e os arquivos estáticos do painel (precisam carregar a tela de login).
func withAuth(h http.Handler, key string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		guarded := strings.HasPrefix(p, "/api/") && !strings.HasSuffix(p, "/chatwoot/webhook")
		if guarded {
			got := r.Header.Get("X-API-Key")
			if got == "" {
				got = r.URL.Query().Get("apiKey")
			}
			if got != key {
				writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
				return
			}
		}
		h.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func clientID(r *http.Request) string {
	if id := r.Header.Get("X-Client-Id"); id != "" {
		return id
	}
	return r.URL.Query().Get("clientId")
}

func (s *server) sessionByID(w http.ResponseWriter, sid string) *Session {
	sess, ok := s.sessions.Get(sid)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such session"})
		return nil
	}
	return sess
}

func (s *server) handleEvents(w http.ResponseWriter, r *http.Request) {
	s.broker.serveSSE(w, r, clientID(r))
}

func (s *server) handleConfig(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"maxCallsPerSession": s.sessions.maxCalls,
	})
}

func (s *server) handleSessionList(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"sessions": s.sessions.infos()})
}

func (s *server) handleSessionCalls(w http.ResponseWriter, r *http.Request) {
	sess := s.sessionByID(w, r.PathValue("sid"))
	if sess == nil {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"active":             sess.reg.count(),
		"maxCallsPerSession": s.sessions.maxCalls,
	})
}

func (s *server) handleSessionCreate(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name string `json:"name"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	name := strings.TrimSpace(body.Name)
	if name == "" {
		name = "Session"
	}
	id, err := s.sessions.Create(name)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": id})
}

func (s *server) handleSessionDelete(w http.ResponseWriter, r *http.Request) {
	if err := s.sessions.Delete(r.Context(), r.PathValue("sid")); err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) handleSessionLogout(w http.ResponseWriter, r *http.Request) {
	if err := s.sessions.Logout(r.Context(), r.PathValue("sid")); err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) handleSessionPair(w http.ResponseWriter, r *http.Request) {
	if err := s.sessions.Pair(r.PathValue("sid")); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) handleStartCall(w http.ResponseWriter, r *http.Request) {
	if sess := s.sessionByID(w, r.PathValue("sid")); sess != nil {
		s.doStartCall(sess, w, r)
	}
}

func (s *server) handleWebRTC(w http.ResponseWriter, r *http.Request) {
	if sess := s.sessionByID(w, r.PathValue("sid")); sess != nil {
		s.doWebRTC(sess, w, r)
	}
}

func (s *server) handleAccept(w http.ResponseWriter, r *http.Request) {
	if sess := s.sessionByID(w, r.PathValue("sid")); sess != nil {
		s.doAccept(sess, w, r)
	}
}

func (s *server) handleReject(w http.ResponseWriter, r *http.Request) {
	if sess := s.sessionByID(w, r.PathValue("sid")); sess != nil {
		s.doReject(sess, w, r)
	}
}

func (s *server) handleEndCall(w http.ResponseWriter, r *http.Request) {
	if sess := s.sessionByID(w, r.PathValue("sid")); sess != nil {
		s.doEndCall(sess, w, r)
	}
}

func (s *server) handleHistory(w http.ResponseWriter, r *http.Request) {
	if sess := s.sessionByID(w, r.PathValue("sid")); sess != nil {
		writeJSON(w, http.StatusOK, map[string]any{"rows": s.broker.historyRows(sess.id, 50)})
	}
}

func (s *server) doStartCall(sess *Session, w http.ResponseWriter, r *http.Request) {
	if sess.client.Store.ID == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "not paired"})
		return
	}
	var body struct {
		Phone      string `json:"phone"`
		DurationMs int    `json:"duration_ms"`
		Record     bool   `json:"record"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.Phone) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "phone required"})
		return
	}
	owner := clientID(r)
	// (removido) regra "1 chamada por operador" — agora o mesmo navegador/aba
	// pode disparar várias ligações na mesma sessão (até -max-calls-per-session).
	if max := s.sessions.maxCalls; max > 0 && sess.reg.count() >= max {
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "max concurrent calls"})
		return
	}
	peer := types.NewJID(normalizePhone(body.Phone), types.DefaultUserServer)

	callID, err := sess.startOutgoing(r.Context(), peer, false)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	s.broker.upsertCall(CallRecord{
		SessionID: sess.id, CallID: callID, Owner: &owner, Direction: "outbound", Peer: peer.String(),
		StartedAt: time.Now().UnixMilli(), Status: StatusRinging,
	})
	writeJSON(w, http.StatusOK, map[string]any{"call": map[string]string{"callId": callID}})
}

func (s *server) doWebRTC(sess *Session, w http.ResponseWriter, r *http.Request) {
	callID := r.PathValue("id")
	ac, ok := sess.reg.get(callID)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such call"})
		return
	}
	var body struct {
		SDPOffer string `json:"sdp_offer"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.SDPOffer == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "sdp_offer required"})
		return
	}
	bridge, answer, err := NewBridge(body.SDPOffer, s.log)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	browserOpus, ocErr := media.NewOpusCodec(48000, 960)
	if ocErr != nil {
		s.log.Warn("browser Opus codec unavailable — call audio disabled", "err", ocErr)
		browserOpus = nil
	}
	bridge.OnBrowserRTP = func(payload []byte) {
		if browserOpus == nil {
			return
		}
		pcm48, err := browserOpus.Decode(payload)
		if err != nil {
			return
		}
		ac.cm.FeedCapturedPCM(media.Downsample48to16(pcm48))
	}
	bridge.OnTerminalICE = func() {
		go sess.terminateCall(callID, core.EndCallReasonUserEnded)
	}
	sess.setBridge(callID, bridge, browserOpus)
	writeJSON(w, http.StatusOK, map[string]string{"sdp_answer": answer})
}

func (s *server) doAccept(sess *Session, w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ac, ok := sess.reg.get(id)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such call"})
		return
	}
	owner := clientID(r)
	if other := s.broker.ownerActiveCall(owner); other != "" && other != id {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "operator already on a call"})
		return
	}
	if !s.broker.setOwner(id, owner) {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "claimed by another client"})
		return
	}
	s.broker.emitIncomingClaimed(sess.id, id, owner)
	if err := ac.cm.AcceptCall(r.Context(), id); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"call": map[string]string{"callId": id}})
}

func (s *server) doReject(sess *Session, w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if ac, ok := sess.reg.get(id); ok {
		_ = ac.cm.RejectCall(r.Context(), id, core.EndCallReasonDeclined)
	}
	sess.removeCall(id)
	s.broker.endCall(id, string(core.EndCallReasonDeclined))
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *server) doEndCall(sess *Session, w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if ac, ok := sess.reg.get(id); ok {
		_ = ac.cm.EndCall(r.Context(), core.EndCallReasonUserEnded)
	}
	sess.removeCall(id)
	s.broker.endCall(id, string(core.EndCallReasonUserEnded))
	w.WriteHeader(http.StatusNoContent)
}

func normalizePhone(p string) string {
	p = strings.TrimSpace(p)
	p = strings.TrimPrefix(p, "+")
	var b strings.Builder
	for _, c := range p {
		if c >= '0' && c <= '9' {
			b.WriteRune(c)
		}
	}
	return b.String()
}

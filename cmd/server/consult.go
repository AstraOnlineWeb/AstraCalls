package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"wacalls/internal/voip/core"
	"wacalls/internal/voip/media"
)

// =============================================================================
// Transferência COM CONSULTA (atendida)
// =============================================================================
//
//  1. A (dono da chamada) → POST /calls/{id}/consult {to: <clientId de B>}:
//     o cliente do WhatsApp vai para a espera (com música) e B recebe o evento
//     SSE call-consult-offer.
//  2. B → POST /calls/{id}/consult/webrtc {sdp_offer}: abre o áudio dele. O
//     servidor liga A ↔ B (o cliente continua na espera e não ouve nada).
//  3. A (ou B) → POST /calls/{id}/consult/complete: B vira o dono, a ponte de B
//     passa a ser a ponte da chamada (cliente ↔ B), a ponte de A é fechada e a
//     chamada sai da espera. Ou POST .../consult/cancel: fecha a ponte de B e o
//     cliente volta a falar com A.
//
// A pode estar em WebRTC ou WebSocket (pcm16); B usa WebRTC. Nada disso toca
// no leg do WhatsApp: só muda quem está na ponte de áudio.

const consultFrame16k = 320 // 20 ms a 16 kHz

type consultLeg struct {
	id, callID, sessionID string
	from, fromName        string
	to, toName            string
	peer                  string
	startedAt             int64

	mu      sync.Mutex
	bBridge *Bridge
	bOpus   media.Codec // decodifica B (quando A está em WS)
	encToB  media.Codec // codifica A (WS, PCM) → B
	bufToB  []float32
	active  atomic.Bool // B conectou o áudio
	closed  atomic.Bool
	// handler: depois de concluída, o RTP de B segue o caminho normal da chamada.
	handler atomic.Pointer[func([]byte)]
	done    atomic.Bool // concluída: o OnTerminalICE de B passa a encerrar a chamada
}

func (c *consultLeg) state() string {
	switch {
	case c.done.Load():
		return "completed"
	case c.active.Load():
		return "active"
	}
	return "offered"
}

func (c *consultLeg) bridgeB() *Bridge {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.bBridge
}

// fromAgentOpus: RTP Opus do navegador de A (WebRTC) → B, sem transcodificar.
func (c *consultLeg) fromAgentOpus(payload []byte) {
	if b := c.bridgeB(); b != nil && c.active.Load() {
		_ = b.WriteOpus(payload, 20*time.Millisecond)
	}
}

// fromAgentPCM: PCM16 16 kHz de A (WebSocket) → Opus 20 ms → B.
func (c *consultLeg) fromAgentPCM(pcm16 []float32) {
	if !c.active.Load() {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.bBridge == nil {
		return
	}
	if c.encToB == nil {
		enc, err := media.NewOpusCodec(48000, 960)
		if err != nil {
			return
		}
		c.encToB = enc
	}
	c.bufToB = append(c.bufToB, pcm16...)
	for len(c.bufToB) >= consultFrame16k {
		frame := c.bufToB[:consultFrame16k]
		if op, err := c.encToB.Encode(media.Upsample16to48(frame)); err == nil && len(op) > 0 {
			_ = c.bBridge.WriteOpus(op, 20*time.Millisecond)
		}
		c.bufToB = c.bufToB[consultFrame16k:]
	}
	if len(c.bufToB) > consultFrame16k*10 {
		c.bufToB = nil
	}
}

// toAgent: RTP Opus de B → A (WebRTC direto; WS decodificado em PCM16).
func (c *consultLeg) toAgent(ac *activeCall, payload []byte) {
	if ws := ac.wsBridge; ws != nil {
		c.mu.Lock()
		if c.bOpus == nil {
			if dec, err := media.NewOpusCodec(48000, 960); err == nil {
				c.bOpus = dec
			}
		}
		dec := c.bOpus
		c.mu.Unlock()
		if dec == nil {
			return
		}
		if pcm48, err := dec.Decode(payload); err == nil {
			_ = ws.WritePCM(media.Downsample48to16(pcm48))
		}
		return
	}
	if b := ac.bridge; b != nil {
		_ = b.WriteOpus(payload, 20*time.Millisecond)
	}
}

// close fecha a ponte de B (sem encerrar a chamada) e os codecs.
func (c *consultLeg) close() {
	if !c.closed.CompareAndSwap(false, true) {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.bBridge != nil && !c.done.Load() {
		c.bBridge.DisableTerminate()
		c.bBridge.Close()
	}
	if c.bOpus != nil {
		c.bOpus.Close()
	}
	if c.encToB != nil {
		c.encToB.Close()
	}
}

func (c *consultLeg) event(state, reason string) map[string]any {
	ev := map[string]any{
		"type": "call-consult-state", "sessionId": c.sessionID, "id": c.callID, "consultId": c.id,
		"state": state, "from": c.from, "fromName": c.fromName, "to": c.to, "toName": c.toName,
	}
	if reason != "" {
		ev["reason"] = reason
	}
	return ev
}

func (c *consultLeg) view() map[string]any {
	v := c.event(c.state(), "")
	delete(v, "type")
	v["startedAt"] = c.startedAt
	v["peer"] = c.peer
	return v
}

// agentRTPHandler é o caminho do RTP (Opus) do navegador do DONO da chamada:
// em consulta vai para o colega; fora dela, para o WhatsApp (+ gravação/espiões).
func agentRTPHandler(ac *activeCall, opus media.Codec) func([]byte) {
	return func(payload []byte) {
		if c := ac.consult.Load(); c != nil {
			c.fromAgentOpus(payload)
			return
		}
		if opus == nil {
			return
		}
		pcm48, err := opus.Decode(payload)
		if err != nil {
			return
		}
		down := media.Downsample48to16(pcm48)
		ac.recorder.writeBrowser(down)
		ac.spies.feed(spySideAgent, down)
		ac.cm.FeedCapturedPCM(down)
	}
}

func (s *server) consultCall(w http.ResponseWriter, r *http.Request) (*Session, *activeCall, bool) {
	sess := s.sessionByID(w, r.PathValue("sid"))
	if sess == nil {
		return nil, nil, false
	}
	id := r.PathValue("id")
	if strings.HasPrefix(id, "sip-") {
		writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "consulta ainda não disponível no modo agente SIP"})
		return nil, nil, false
	}
	ac, ok := sess.reg.get(id)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such call"})
		return nil, nil, false
	}
	return sess, ac, true
}

// POST /calls/{id}/consult {to, fromLabel, moh, moh_url}
func (s *server) handleConsultStart(w http.ResponseWriter, r *http.Request) {
	sess, ac, ok := s.consultCall(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	var b struct {
		To        string `json:"to"`
		FromLabel string `json:"fromLabel"`
		MOH       *bool  `json:"moh"`
		MOHURL    string `json:"moh_url"`
	}
	_ = json.NewDecoder(r.Body).Decode(&b)
	to := strings.TrimSpace(b.To)
	from := clientID(r)
	if to == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "to (clientId do colega) obrigatório"})
		return
	}
	if to == from {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "não dá para consultar a si mesmo"})
		return
	}
	if !s.broker.isConnected(sess.id, to) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "atendente de destino não está conectado", "code": "agent_offline"})
		return
	}
	if ac.bridge == nil && ac.wsBridge == nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "a chamada não tem áudio de atendente conectado (/webrtc ou /ws)"})
		return
	}
	peer := ""
	if cr, ok := s.broker.getCall(id); ok && cr != nil {
		peer = cr.Peer
		if cr.Status != StatusConnected {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "a chamada não está ativa"})
			return
		}
	}
	fromName := strings.TrimSpace(b.FromLabel)
	if fromName == "" {
		fromName = s.broker.labelOf(from)
	}
	c := &consultLeg{
		id: newSpyID(), callID: id, sessionID: sess.id, from: from, fromName: fromName,
		to: to, toName: s.broker.labelOf(to), peer: peer, startedAt: time.Now().UnixMilli(),
	}
	s.transfers().drop(id, "consulta")
	if !ac.consult.CompareAndSwap(nil, c) {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "já há uma consulta em andamento nesta chamada"})
		return
	}
	moh := b.MOH == nil || *b.MOH
	var loop []float32
	if moh {
		loop = sess.mohFor(b.MOHURL)
	}
	_ = ac.cm.HoldWith(moh, loop) // já em espera = ok
	offer := c.event("offered", "")
	offer["type"] = "call-consult-offer"
	offer["peer"] = peer
	offer["offeredAt"] = c.startedAt
	s.broker.broadcastForSessionTargeted(sess.id, to, offer)
	s.broker.broadcastForSession(sess.id, c.event("offered", ""))
	s.log.Info("consulta iniciada", "call", id, "from", from, "to", to)
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "consult": c.view()})
}

// GET /calls/{id}/consult
func (s *server) handleConsultGet(w http.ResponseWriter, r *http.Request) {
	_, ac, ok := s.consultCall(w, r)
	if !ok {
		return
	}
	c := ac.consult.Load()
	if c == nil {
		writeJSON(w, http.StatusOK, map[string]any{"consult": nil})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"consult": c.view()})
}

// POST /calls/{id}/consult/webrtc {sdp_offer} — o colega (B) conecta o áudio.
func (s *server) handleConsultWebRTC(w http.ResponseWriter, r *http.Request) {
	sess, ac, ok := s.consultCall(w, r)
	if !ok {
		return
	}
	c := ac.consult.Load()
	if c == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "nenhuma consulta em andamento"})
		return
	}
	if me := clientID(r); me != "" && me != c.to {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "a consulta é para outro atendente"})
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
	callID := c.callID
	bridge.OnBrowserRTP = func(payload []byte) {
		if h := c.handler.Load(); h != nil { // concluída: B é o atendente da chamada
			(*h)(payload)
			return
		}
		if ac.consult.Load() == c {
			c.toAgent(ac, payload)
		}
	}
	bridge.OnTerminalICE = func() {
		if c.done.Load() {
			go sess.terminateCall(callID, core.EndCallReasonUserEnded)
			return
		}
		go s.cancelConsult(sess, ac, c, "colega desconectou")
	}
	c.mu.Lock()
	old := c.bBridge
	c.bBridge = bridge
	c.mu.Unlock()
	if old != nil {
		old.DisableTerminate()
		old.Close()
	}
	if c.closed.Load() || ac.consult.Load() != c {
		bridge.DisableTerminate()
		bridge.Close()
		writeJSON(w, http.StatusConflict, map[string]string{"error": "a consulta foi encerrada"})
		return
	}
	c.active.Store(true)
	s.broker.broadcastForSession(sess.id, c.event("active", ""))
	writeJSON(w, http.StatusOK, map[string]string{"sdp_answer": answer, "consultId": c.id})
}

// POST /calls/{id}/consult/complete — cliente ↔ B, A sai.
func (s *server) handleConsultComplete(w http.ResponseWriter, r *http.Request) {
	sess, ac, ok := s.consultCall(w, r)
	if !ok {
		return
	}
	c := ac.consult.Load()
	if c == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "nenhuma consulta em andamento"})
		return
	}
	bb := c.bridgeB()
	if bb == nil || !c.active.Load() {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "o colega ainda não conectou o áudio da consulta"})
		return
	}
	opusB, err := media.NewOpusCodec(48000, 960)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "codec Opus indisponível"})
		return
	}
	if !ac.consult.CompareAndSwap(c, nil) {
		opusB.Close()
		writeJSON(w, http.StatusConflict, map[string]string{"error": "a consulta já foi encerrada"})
		return
	}
	h := agentRTPHandler(ac, opusB)
	c.handler.Store(&h)
	c.done.Store(true)
	sess.setBridge(c.callID, bb, opusB) // fecha a ponte de A sem derrubar a chamada
	c.close()
	owner := c.to
	s.broker.reassignOwner(c.callID, &owner)
	s.broker.emitTransferClaimed(sess.id, c.callID, owner)
	_ = ac.cm.Resume()
	s.broker.broadcastForSession(sess.id, c.event("completed", ""))
	s.log.Info("consulta concluída (transferência atendida)", "call", c.callID, "from", c.from, "to", c.to)
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "owner": owner, "consult": c.view()})
}

// POST /calls/{id}/consult/cancel — B sai, cliente ↔ A de novo.
func (s *server) handleConsultCancel(w http.ResponseWriter, r *http.Request) {
	sess, ac, ok := s.consultCall(w, r)
	if !ok {
		return
	}
	c := ac.consult.Load()
	if c == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "nenhuma consulta em andamento"})
		return
	}
	var b struct {
		Reason string `json:"reason"`
	}
	_ = json.NewDecoder(r.Body).Decode(&b)
	reason := strings.TrimSpace(b.Reason)
	if reason == "" {
		if clientID(r) == c.to {
			reason = "recusada pelo colega"
		} else {
			reason = "cancelada"
		}
	}
	if err := s.cancelConsult(sess, ac, c, reason); err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "state": "cancelled"})
}

func (s *server) cancelConsult(sess *Session, ac *activeCall, c *consultLeg, reason string) error {
	if !ac.consult.CompareAndSwap(c, nil) {
		return errors.New("a consulta já foi encerrada")
	}
	c.close()
	_ = ac.cm.Resume()
	s.broker.broadcastForSession(sess.id, c.event("cancelled", reason))
	s.log.Info("consulta cancelada", "call", c.callID, "reason", reason)
	return nil
}

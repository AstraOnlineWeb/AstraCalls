package main

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"

	"wacalls/internal/voip/media"
)

// Modo agente do SIP: um INVITE entrante (alguém ligando para o ramal) é ATENDIDO
// pelo AstraCalls (UAS) e entregue ao agente de voz exatamente como uma chamada
// WhatsApp recebida — evento "incoming" no SSE, POST /accept e o mesmo WS pcm16
// (GET /calls/{id}/ws). Nunca origina chamada no WhatsApp.

// sipAgentCall é uma chamada SIP entrante atendida para o agente. O id
// ("sip-…") é a chave em todas as rotas de chamada por id.
type sipAgentCall struct {
	gw        *SIPGateway
	id        string
	sessionID string
	sipCallID string
	phone     string
	name      string
	from      string
	to        string
	codec     string
	startedAt int64

	dlg     *sipgo.DialogServerSession
	rtp     *SIPRTPBridge
	sdp     []byte
	contact sip.ContactHeader
	autoAns bool

	// settled fecha quando o INVITE tem resposta final concluída (200+ACK, erro
	// ou fim): o handler do sipgo precisa ficar bloqueado até lá, senão a
	// transação do INVITE é terminada ao retornar e o 200 OK não sai.
	settled     chan struct{}
	settledOnce sync.Once

	mu sync.Mutex
	ws *wsBridge
	// ponte WebRTC do navegador (atendente humano que assumiu via transferência).
	browser     *Bridge
	browserOpus media.Codec
	browserBuf  []float32 // PCM 16 kHz acumulado até 20 ms (quadro Opus)
	held        bool
	answered    bool
	ended       bool
	ringTimer   *time.Timer
}

func (gw *SIPGateway) agentCall(sessionID, id string) *sipAgentCall {
	gw.mu.RLock()
	defer gw.mu.RUnlock()
	c, ok := gw.agentCalls[id]
	if !ok || (sessionID != "" && c.sessionID != sessionID) {
		return nil
	}
	return c
}

// agentCallBySIP acha a chamada pelo Call-ID do SIP enquanto o DIÁLOGO existe —
// inclusive logo depois de encerrada do nosso lado (o ACK do 200 ou a resposta
// ao BYE ainda podem chegar).
func (gw *SIPGateway) agentCallBySIP(sipCallID string) *sipAgentCall {
	gw.mu.RLock()
	defer gw.mu.RUnlock()
	return gw.agentDialogs[sipCallID]
}

// agentCount devolve quantas chamadas do modo agente a sessão tem abertas
// (somam no mesmo limite WACALLS_MAX_CALLS das chamadas WhatsApp).
func (gw *SIPGateway) agentCount(sessionID string) int {
	gw.mu.RLock()
	defer gw.mu.RUnlock()
	n := 0
	for _, c := range gw.agentCalls {
		if c.sessionID == sessionID {
			n++
		}
	}
	return n
}

// wantsAgent decide se o INVITE vai para o agente, conforme o inboundMode da
// sessão. Em "auto", só quando ligaram para o próprio ramal/usuário SIP.
func wantsAgent(sess *Session, req *sip.Request) bool {
	cfg := sess.sipExtSnapshot()
	switch cfg.Agent.mode() {
	case sipInboundAgent:
		return true
	case sipInboundAuto:
		target := req.Recipient.User
		if to := req.To(); to != nil && target == "" {
			target = to.Address.User
		}
		return target == "" || (cfg.User != "" && target == cfg.User) || (sess.SIPUser != "" && target == sess.SIPUser)
	}
	return false
}

// callerIdentity tira o número/nome do chamador: P-Asserted-Identity tem
// prioridade sobre o From. phone = só dígitos.
func callerIdentity(req *sip.Request) (phone, name, from string) {
	if f := req.From(); f != nil {
		from = f.Address.String()
		name = strings.Trim(f.DisplayName, `" `)
		phone = digitsOnly(f.Address.User)
	}
	if h := req.GetHeader("P-Asserted-Identity"); h != nil {
		v := h.Value()
		if i := strings.Index(v, "sip:"); i >= 0 {
			u := v[i+4:]
			if j := strings.IndexAny(u, "@;>"); j >= 0 {
				u = u[:j]
			}
			if d := digitsOnly(u); d != "" {
				phone = d
			}
		} else if i := strings.Index(v, "tel:"); i >= 0 {
			u := v[i+4:]
			if j := strings.IndexAny(u, ";>"); j >= 0 {
				u = u[:j]
			}
			if d := digitsOnly(u); d != "" {
				phone = d
			}
		}
	}
	if name == phone {
		name = ""
	}
	return
}

// handleAgentInvite atende um INVITE novo no modo agente.
func (gw *SIPGateway) handleAgentInvite(sess *Session, req *sip.Request, tx sip.ServerTransaction) {
	if max := gw.sessions.maxCalls; max > 0 && sess.activeCallCount() >= max {
		gw.reply(tx, req, 486, "Busy Here")
		return
	}
	dlg, err := gw.dialogUA.ReadInvite(req, tx)
	if err != nil {
		gw.log.Warn("SIP agent: INVITE inválido", "err", err)
		gw.reply(tx, req, 400, "Bad Request")
		return
	}
	offer := parseSDPOffer(string(req.Body()))
	pt, ok := offer.pickG711()
	if !ok {
		gw.log.Warn("SIP agent: oferta sem G.711", "sip_call_id", req.CallID().Value())
		go func() { _ = dlg.Respond(488, "Not Acceptable Here", nil) }()
		return
	}
	_ = dlg.Respond(100, "Trying", nil)

	id := "sip-" + generateRandomString(12)
	rtpBr, err := NewSIPRTPBridge(id, offer.addr, gw.log)
	if err != nil {
		gw.log.Error("SIP agent: RTP falhou", "err", err)
		go func() { _ = dlg.Respond(500, "Internal Server Error", nil) }()
		return
	}
	rtpBr.SetCodec(pt, offer.dtmfPT)
	rtpBr.StartPacer()

	localIP := getLocalIP()
	phone, name, from := callerIdentity(req)
	to := req.Recipient.User
	if to == "" && req.To() != nil {
		to = req.To().Address.User
	}
	cfg := sess.sipExtSnapshot()
	c := &sipAgentCall{
		gw: gw, id: id, sessionID: sess.id, sipCallID: req.CallID().Value(),
		phone: phone, name: name, from: from, to: to, codec: g711Name(pt),
		startedAt: time.Now().UnixMilli(),
		dlg:       dlg, rtp: rtpBr,
		sdp: []byte(buildSIPSDPAnswer(localIP, rtpBr.LocalPort(), pt, offer.dtmfPT)),
		contact: sip.ContactHeader{Address: sip.Uri{
			Scheme: "sip", User: to, Host: localIP, Port: sipAdvertisePort(),
		}},
		autoAns: cfg.Agent.AutoAnswer,
		settled: make(chan struct{}),
	}
	rtpBr.SetHandlers(func(pcm []float32) {
		if ws := c.wsConn(); ws != nil {
			_ = ws.WritePCM(pcm)
		}
		c.writeBrowser(pcm)
	}, func(d string) {
		if ws := c.wsConn(); ws != nil {
			ws.SendEvent(map[string]any{"type": "dtmf", "digit": d})
		}
	})

	// 180 antes de publicar a chamada: depois disso a ponte pode aceitar a
	// qualquer momento, e o diálogo do sipgo não aceita respostas concorrentes.
	_ = dlg.Respond(180, "Ringing", nil)

	gw.mu.Lock()
	gw.agentCalls[id] = c
	gw.agentDialogs[c.sipCallID] = c
	gw.mu.Unlock()

	gw.log.Info("SIP agent: chamada entrante", "id", id, "session", sess.id,
		"phone", phone, "to", to, "codec", c.codec, "sip_call_id", c.sipCallID)

	b := gw.sessions.broker
	b.upsertCall(CallRecord{
		SessionID: sess.id, CallID: id, Direction: "inbound", Peer: phone,
		StartedAt: c.startedAt, Status: StatusRinging, Source: "sip",
	})
	b.emitIncomingSIP(sess.id, id, phone, name, map[string]any{
		"callId": c.sipCallID, "from": from, "to": to, "codec": c.codec,
	})
	// o chamador desligou (CANCEL antes do 200, ou BYE depois): o diálogo termina.
	go func() {
		<-dlg.Context().Done()
		c.mu.Lock()
		answered := c.answered
		c.mu.Unlock()
		if answered {
			c.end("remote_hangup", 0)
		} else {
			c.end("caller_cancelled", 0)
		}
		gw.mu.Lock()
		delete(gw.agentDialogs, c.sipCallID)
		gw.mu.Unlock()
	}()

	c.mu.Lock()
	c.ringTimer = time.AfterFunc(cfg.Agent.ringTimeout(), func() { c.end("timeout", 480) })
	c.mu.Unlock()

	if c.autoAns {
		if err := c.answer(); err != nil {
			gw.log.Warn("SIP agent: auto-answer falhou", "id", id, "err", err)
		}
	}
	<-c.settled
}

// forgetDialog tira o diálogo do índice por Call-ID (depois do BYE/resposta final).
func (c *sipAgentCall) forgetDialog() {
	c.gw.mu.Lock()
	delete(c.gw.agentDialogs, c.sipCallID)
	c.gw.mu.Unlock()
}

func (c *sipAgentCall) settle() { c.settledOnce.Do(func() { close(c.settled) }) }

func (c *sipAgentCall) wsConn() *wsBridge {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ws
}

// answer manda o 200 OK com o SDP (idempotente).
func (c *sipAgentCall) answer() error {
	c.mu.Lock()
	if c.ended {
		c.mu.Unlock()
		return errSIPAgentEnded
	}
	if c.answered {
		c.mu.Unlock()
		return nil
	}
	c.answered = true
	if c.ringTimer != nil {
		c.ringTimer.Stop()
	}
	c.mu.Unlock()

	ct := sip.NewHeader("Content-Type", "application/sdp")
	contact := c.contact
	// o 200 OK fica retransmitindo até o ACK chegar: não segura a requisição HTTP.
	go func() {
		defer c.settle()
		if err := c.dlg.Respond(200, "OK", c.sdp, ct, &contact); err != nil {
			c.gw.log.Warn("SIP agent: 200 OK sem ACK", "id", c.id, "err", err)
			c.end("error", 0)
		}
	}()
	c.gw.log.Info("SIP agent: atendida", "id", c.id, "sip_call_id", c.sipCallID)

	b := c.gw.sessions.broker
	rec := CallRecord{
		SessionID: c.sessionID, CallID: c.id, Direction: "inbound", Peer: c.phone,
		StartedAt: c.startedAt, Status: StatusConnected, Source: "sip",
	}
	if existing, ok := b.getCall(c.id); ok {
		rec.Owner = existing.Owner
	}
	b.upsertCall(rec)
	if ws := c.wsConn(); ws != nil {
		ws.SendEvent(map[string]any{"type": "call-status", "status": string(StatusConnected)})
	}
	return nil
}

type sipAgentErr string

func (e sipAgentErr) Error() string { return string(e) }

const errSIPAgentEnded = sipAgentErr("call already ended")

// end encerra a chamada uma única vez. code: resposta final ao INVITE se ainda
// não foi atendida (486, 480, 603…); 0 = não responde (o diálogo já terminou).
// Atendida e encerrada por nós (reason agent_hangup) manda BYE.
func (c *sipAgentCall) end(reason string, code int) {
	c.mu.Lock()
	if c.ended {
		c.mu.Unlock()
		return
	}
	c.ended = true
	answered := c.answered
	if c.ringTimer != nil {
		c.ringTimer.Stop()
	}
	ws := c.ws
	c.ws = nil
	br, oc := c.browser, c.browserOpus
	c.browser, c.browserOpus = nil, nil
	c.mu.Unlock()
	if br != nil {
		br.DisableTerminate()
		br.Close()
	}
	if oc != nil {
		oc.Close()
	}

	switch {
	case !answered && code > 0:
		reasonText := map[int]string{480: "Temporarily Unavailable", 486: "Busy Here", 603: "Decline"}[code]
		// respostas finais não-2xx esperam o ACK: fora da goroutine do chamador.
		go func() {
			defer c.settle()
			_ = c.dlg.Respond(code, reasonText, nil)
			c.forgetDialog()
		}()
	case answered && (reason == "agent_hangup" || reason == "error"):
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := c.dlg.Bye(ctx); err != nil {
				c.gw.log.Warn("SIP agent: BYE falhou", "id", c.id, "err", err)
			}
			c.forgetDialog()
		}()
	}
	if answered || code == 0 {
		c.settle()
	}
	c.rtp.Close()
	if ws != nil {
		ws.SendEvent(map[string]any{"type": "call-ended", "reason": reason})
		ws.DisableTerminate()
		ws.Close()
	}

	c.gw.mu.Lock()
	delete(c.gw.agentCalls, c.id)
	c.gw.mu.Unlock()
	c.gw.sessions.broker.endCall(c.id, reason)
	c.gw.log.Info("SIP agent: encerrada", "id", c.id, "reason", reason)
}

// ===== integração com as rotas HTTP de chamada =====

// sipAgentAccept: POST /calls/{id}/accept numa chamada do modo agente.
func (s *server) sipAgentAccept(c *sipAgentCall, w http.ResponseWriter, r *http.Request) {
	owner := clientID(r)
	if !s.broker.setOwner(c.id, owner) {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "claimed by another client"})
		return
	}
	s.broker.emitIncomingClaimed(c.sessionID, c.id, owner)
	s.log.Info("SIP agent: accept", "id", c.id, "owner", owner)
	if err := c.answer(); err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"call": map[string]string{"callId": c.id}})
}

// sipAgentReject: POST /calls/{id}/reject → 486 (ou 603 com {"reason":"decline"}).
func (s *server) sipAgentReject(c *sipAgentCall, w http.ResponseWriter, r *http.Request) {
	var body struct {
		Reason string `json:"reason"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	c.mu.Lock()
	answered := c.answered
	c.mu.Unlock()
	if answered {
		c.end("agent_hangup", 0)
	} else if strings.EqualFold(body.Reason, "decline") {
		c.end("declined", 603)
	} else {
		c.end("declined", 486)
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// sipAgentWS: GET /calls/{id}/ws — mesmo contrato "pcm16" da chamada WhatsApp.
// Texto aceito da ponte: {"type":"clear"} esvazia o áudio ainda não tocado.
func (s *server) sipAgentWS(c *sipAgentCall, w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		Subprotocols:       []string{"pcm16"},
		InsecureSkipVerify: true,
	})
	if err != nil {
		s.log.Warn("sip agent ws: upgrade failed", "err", err, "call", c.id)
		return
	}
	events := r.URL.Query().Get("events") == "1" || r.URL.Query().Get("events") == "true"
	bridge := newWSBridge(conn, s.log, events)
	bridge.OnBrowserPCM = func(pcm16 []float32) { _ = c.rtp.WritePCM(pcm16) }
	bridge.OnText = func(data []byte) {
		var ev struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(data, &ev) == nil && ev.Type == "clear" {
			c.rtp.ClearOutput()
		}
	}
	bridge.OnTerminalWS = func() { go c.end("agent_hangup", 486) }

	c.mu.Lock()
	if c.ended {
		c.mu.Unlock()
		bridge.SendEvent(map[string]any{"type": "call-ended", "reason": "ended"})
		bridge.Close()
		return
	}
	old := c.ws
	c.ws = bridge
	answered := c.answered
	held := c.held
	// a ponte WS assume o áudio: solta a do navegador, se houver.
	oldBr, oldOC := c.browser, c.browserOpus
	c.browser, c.browserOpus, c.browserBuf = nil, nil, nil
	c.mu.Unlock()
	if old != nil {
		old.DisableTerminate()
		old.Close()
	}
	if oldBr != nil {
		oldBr.DisableTerminate()
		oldBr.Close()
	}
	if oldOC != nil {
		oldOC.Close()
	}
	if held {
		c.setHold(false, false)
	}
	if answered {
		bridge.SendEvent(map[string]any{"type": "call-status", "status": string(StatusConnected)})
	}

	s.log.Info("sip agent ws: connected", "call", c.id, "events", events)
	go bridge.keepAlive()
	bridge.readLoop()
	s.log.Info("sip agent ws: disconnected", "call", c.id)
}

// ===== transferência para atendente humano =====

// browserFrame16k = 20 ms a 16 kHz (vira 960 amostras a 48 kHz, quadro Opus válido).
const browserFrame16k = 320

// writeBrowser manda o áudio de quem ligou (16 kHz) para o navegador do atendente.
func (c *sipAgentCall) writeBrowser(pcm []float32) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.browser == nil || c.browserOpus == nil {
		return
	}
	c.browserBuf = append(c.browserBuf, pcm...)
	for len(c.browserBuf) >= browserFrame16k {
		frame := media.Upsample16to48(c.browserBuf[:browserFrame16k])
		c.browserBuf = c.browserBuf[:copy(c.browserBuf, c.browserBuf[browserFrame16k:])]
		if opus, err := c.browserOpus.Encode(frame); err == nil && len(opus) > 0 {
			_ = c.browser.WriteOpus(opus, 20*time.Millisecond)
		}
	}
}

// setHold põe/tira a chamada da espera: em espera quem ligou ouve música (moh)
// e o áudio do agente/atendente não passa.
func (c *sipAgentCall) setHold(held, moh bool) {
	c.mu.Lock()
	c.held = held
	c.mu.Unlock()
	c.rtp.SetHold(held, moh)
	b := c.gw.sessions.broker
	if cr, ok := b.getCall(c.id); ok && cr != nil {
		rec := *cr
		rec.Held = held
		b.upsertCall(rec)
	}
}

// sipAgentHold / sipAgentResume: POST /calls/{id}/hold {"moh":bool} e /resume.
func (s *server) sipAgentHold(c *sipAgentCall, w http.ResponseWriter, r *http.Request) {
	var body struct {
		MOH *bool `json:"moh"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if !c.isAnswered() {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "call not answered"})
		return
	}
	moh := body.MOH == nil || *body.MOH
	c.setHold(true, moh)
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "held": true, "moh": moh})
}

func (s *server) sipAgentResume(c *sipAgentCall, w http.ResponseWriter, r *http.Request) {
	if !c.isAnswered() {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "call not answered"})
		return
	}
	c.setHold(false, false)
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "held": false})
}

// detachAgentWS solta a ponte WS atual (a IA) sem encerrar a chamada.
func (c *sipAgentCall) detachAgentWS(reason string) {
	c.mu.Lock()
	ws := c.ws
	c.ws = nil
	c.mu.Unlock()
	if ws != nil {
		ws.SendEvent(map[string]any{"type": "call-transferred", "reason": reason})
		ws.DisableTerminate()
		ws.Close()
	}
}

func (c *sipAgentCall) isAnswered() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.answered && !c.ended
}

// sipAgentTransfer: POST /calls/{id}/transfer numa chamada SIP do modo agente.
// Transferência CEGA para um atendente: põe em espera com música (opcional
// {"moh":false}), desliga a ponte WS da IA (ela recebe {"type":"call-transferred"}
// e o fechamento NÃO derruba a ligação), libera o dono e manda
// call-transfer-offer (para {"to":"<clientId>"} ou para todos da conta).
// O atendente assume com /pickup (ou /accept) e abre o áudio com /webrtc ou /ws.
func (s *server) sipAgentTransfer(c *sipAgentCall, w http.ResponseWriter, r *http.Request) {
	var body struct {
		To  string `json:"to"`
		MOH *bool  `json:"moh"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if !c.isAnswered() {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "call not answered"})
		return
	}
	moh := body.MOH == nil || *body.MOH
	c.setHold(true, moh)
	c.detachAgentWS("transfer")
	s.broker.reassignOwner(c.id, nil)
	to := strings.TrimSpace(body.To)
	s.broker.emitTransferOffer(c.sessionID, c.id, c.phone, clientID(r), to)
	s.log.Info("SIP agent: transferida", "id", c.id, "to", to)
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "transferred": true, "to": to})
}

// sipAgentPickup: POST /calls/{id}/pickup — o atendente assume a chamada
// transferida. A chamada SAI da espera quando o áudio dele conecta (/webrtc ou
// /ws) ou com /resume.
func (s *server) sipAgentPickup(c *sipAgentCall, w http.ResponseWriter, r *http.Request) {
	if !c.isAnswered() {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "call not answered"})
		return
	}
	owner := clientID(r)
	if !s.broker.reassignOwner(c.id, &owner) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such call"})
		return
	}
	s.broker.emitTransferClaimed(c.sessionID, c.id, owner)
	s.log.Info("SIP agent: pickup", "id", c.id, "owner", owner)
	writeJSON(w, http.StatusOK, map[string]any{"call": map[string]string{"callId": c.id}})
}

// sipAgentWebRTC: POST /calls/{id}/webrtc {sdp_offer} — áudio da chamada SIP no
// navegador (Opus/WebRTC). Substitui a ponte WS da IA, se houver, e tira da espera.
func (s *server) sipAgentWebRTC(c *sipAgentCall, w http.ResponseWriter, r *http.Request) {
	var body struct {
		SDPOffer string `json:"sdp_offer"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.SDPOffer == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "sdp_offer required"})
		return
	}
	br, answer, err := NewBridge(body.SDPOffer, s.log)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	oc, ocErr := media.NewOpusCodec(48000, 960)
	if ocErr != nil {
		s.log.Warn("browser Opus codec unavailable — call audio disabled", "err", ocErr)
		oc = nil
	}
	br.OnBrowserRTP = func(payload []byte) {
		if oc == nil {
			return
		}
		pcm48, err := oc.Decode(payload)
		if err != nil {
			return
		}
		_ = c.rtp.WritePCM(media.Downsample48to16(pcm48))
	}
	owner := clientID(r)
	br.OnICEState = func(st string) {
		s.log.Info("SIP agent: ICE do navegador", "id", c.id, "owner", owner, "state", st)
	}
	br.OnTerminalICE = func() { go s.sipAgentBrowserGone(c, br) }

	c.mu.Lock()
	if c.ended {
		c.mu.Unlock()
		br.DisableTerminate()
		br.Close()
		if oc != nil {
			oc.Close()
		}
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such call"})
		return
	}
	oldBr, oldOC := c.browser, c.browserOpus
	c.browser, c.browserOpus, c.browserBuf = br, oc, nil
	held := c.held
	c.mu.Unlock()
	if oldBr != nil {
		oldBr.DisableTerminate()
		oldBr.Close()
	}
	if oldOC != nil {
		oldOC.Close()
	}
	c.detachAgentWS("webrtc")
	if held {
		c.setHold(false, false)
	}
	_ = c.answer() // atender pelo navegador direto também vale
	s.log.Info("SIP agent: webrtc do navegador", "id", c.id, "owner", owner, "wasHeld", held)
	writeJSON(w, http.StatusOK, map[string]string{"sdp_answer": answer})
}

// sipAgentBrowserGone trata o fim do WebRTC do navegador. Se o ICE NUNCA
// conectou (rede/NAT do operador, offer ruim), o operador não chegou a ouvir
// nada: a ligação NÃO cai — volta para a espera com música e o painel recebe
// call-webrtc-failed para tentar de novo. Se já estava conectado, é o operador
// saindo (aba fechada/queda) e a ligação encerra como antes.
func (s *server) sipAgentBrowserGone(c *sipAgentCall, br *Bridge) {
	c.mu.Lock()
	current := c.browser == br
	var oc media.Codec
	if current {
		oc = c.browserOpus
		c.browser, c.browserOpus, c.browserBuf = nil, nil, nil
	}
	ended := c.ended
	c.mu.Unlock()
	if !current || ended {
		return
	}
	br.DisableTerminate()
	br.Close()
	if oc != nil {
		oc.Close()
	}
	if !br.WasConnected() {
		s.log.Warn("SIP agent: WebRTC do navegador falhou antes de conectar; ligação volta para espera", "id", c.id)
		c.setHold(true, true)
		s.broker.emitWebRTCFailed(c.sessionID, c.id, "ice_failed")
		return
	}
	c.end("agent_hangup", 486)
}

// ===== re-INVITE / ACK / OPTIONS =====

// agentReInvite responde um re-INVITE (hold, refresh de sessão) do diálogo com o
// mesmo SDP e atualiza o destino RTP.
func (gw *SIPGateway) agentReInvite(c *sipAgentCall, req *sip.Request, tx sip.ServerTransaction) {
	if addr := parseSDPOffer(string(req.Body())).addr; addr != "" {
		if ua, err := net.ResolveUDPAddr("udp", addr); err == nil {
			c.rtp.mu.Lock()
			c.rtp.remote = ua
			c.rtp.mu.Unlock()
		}
	}
	res := sip.NewResponseFromRequest(req, 200, "OK", c.sdp)
	res.AppendHeader(sip.NewHeader("Content-Type", "application/sdp"))
	contact := c.contact
	res.AppendHeader(&contact)
	_ = tx.Respond(res)
}

// handleAck confirma o diálogo do modo agente. ACK de outras chamadas não tem
// resposta (sem handler o sipgo logava "handler not found" e tentava um 405).
func (gw *SIPGateway) handleAck(req *sip.Request, tx sip.ServerTransaction) {
	if c := gw.agentCallBySIP(req.CallID().Value()); c != nil {
		_ = c.dlg.ReadAck(req, tx)
		return
	}
	// ACK do 200 de uma chamada SIP->WhatsApp: confirma o diálogo (para o 200
	// de retransmitir).
	gw.mu.RLock()
	sc, ok := gw.activeCalls[req.CallID().Value()]
	gw.mu.RUnlock()
	if ok && sc.dlg != nil {
		_ = sc.dlg.ReadAck(req, tx)
	}
}

// handleOptions responde ao qualify/keep-alive do PBX.
func (gw *SIPGateway) handleOptions(req *sip.Request, tx sip.ServerTransaction) {
	res := sip.NewResponseFromRequest(req, 200, "OK", nil)
	res.AppendHeader(sip.NewHeader("Allow", "INVITE, ACK, BYE, CANCEL, OPTIONS, REGISTER"))
	_ = tx.Respond(res)
}

package main

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/emiago/sipgo"
	"github.com/google/uuid"
)

// Uma chamada entre o PABX (perna RTP) e UM consumidor por vez: o navegador do
// atendente (WebRTC) ou a IA (WS pcm16). Trocar de consumidor (a IA passa para o
// atendente) não derruba a chamada; o consumidor sair sem troca, derruba.
const (
	statusRinging   = "ringing"
	statusConnected = "connected"
	statusEnded     = "ended"

	frameBytes8k  = 160 // 20 ms a 8 kHz (G.711)
	frameBytes16k = 640 // 20 ms a 16 kHz PCM16
	maxPCMBuffer  = 16000 * 2 * 5
)

var (
	errNotRinging = errors.New("a chamada não está tocando")
	errNotActive  = errors.New("a chamada não está em andamento")
)

type Call struct {
	ID         string
	LineID     string
	AccountID  int
	InboxID    int
	Direction  string // inbound | outbound
	Peer       string // número do outro lado
	Name       string
	Extension  string // ramal do agente quando a chamada é dele
	AgentEmail string
	StartedAt  time.Time
	sipCallID  string

	s   *server
	leg *rtpLeg

	mu           sync.Mutex
	status       string
	owner        *string
	srv          *sipgo.DialogServerSession // entrada
	cli          *sipgo.DialogClientSession // saída
	cancelDial   context.CancelFunc
	webrtc       *webrtcLeg
	ws           *wsLeg
	pcmBuf       []byte
	lastSample   int16
	transferring bool
	ended        bool
	ringTimer    *time.Timer
	stop         chan struct{}

	// settled fecha quando a entrada recebe a resposta final (atendida, recusada ou
	// encerrada). O handler do INVITE segura até aqui: o sipgo encerra a transação
	// assim que o handler retorna.
	settled    chan struct{}
	settleOnce sync.Once
}

func (c *Call) settle() { c.settleOnce.Do(func() { close(c.settled) }) }

type callManager struct {
	mu        sync.RWMutex
	byID      map[string]*Call
	bySIPCall map[string]*Call
}

func newCallManager() *callManager {
	return &callManager{byID: map[string]*Call{}, bySIPCall: map[string]*Call{}}
}

func (m *callManager) add(c *Call) {
	m.mu.Lock()
	m.byID[c.ID] = c
	m.bySIPCall[c.sipCallID] = c
	m.mu.Unlock()
}

func (m *callManager) remove(c *Call) {
	m.mu.Lock()
	delete(m.byID, c.ID)
	delete(m.bySIPCall, c.sipCallID)
	m.mu.Unlock()
}

func (m *callManager) get(id string) *Call {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.byID[id]
}

func (m *callManager) bySIP(sipCallID string) *Call {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.bySIPCall[sipCallID]
}

func (m *callManager) countForLine(lineID string) int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	n := 0
	for _, c := range m.byID {
		if c.LineID == lineID {
			n++
		}
	}
	return n
}

// listEvent: o retrato `call-list` que o softphone e o listener usam para
// reconciliar (accountID 0 = tudo).
func (m *callManager) listEvent(accountID int) map[string]any {
	m.mu.RLock()
	defer m.mu.RUnlock()
	calls := []CallRecord{}
	for _, c := range m.byID {
		if accountID == 0 || c.AccountID == accountID {
			calls = append(calls, c.record())
		}
	}
	return map[string]any{"type": "call-list", "calls": calls}
}

func (s *server) newCall(line Line, direction, peer, name string, agent *Agent, sipCallID string) (*Call, error) {
	leg, err := newRTPLeg(s.ports)
	if err != nil {
		return nil, err
	}
	c := &Call{
		ID: uuid.NewString(), LineID: line.ID, AccountID: line.AccountID, InboxID: line.InboxID,
		Direction: direction, Peer: peer, Name: name, StartedAt: time.Now(), sipCallID: sipCallID,
		s: s, leg: leg, status: statusRinging, stop: make(chan struct{}), settled: make(chan struct{}),
	}
	if agent != nil {
		c.Extension, c.AgentEmail = agent.Extension, agent.Email
	}
	leg.onPayload = c.fromPBX
	s.calls.add(c)
	go c.pace()
	return c, nil
}

func (c *Call) record() CallRecord {
	c.mu.Lock()
	defer c.mu.Unlock()
	return CallRecord{
		SessionID: c.LineID, CallID: c.ID, Owner: c.owner, Direction: c.Direction, Peer: c.Peer,
		StartedAt: c.StartedAt.UnixMilli(), Status: c.status, Extension: c.Extension, AgentEmail: c.AgentEmail,
	}
}

func (c *Call) currentStatus() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.status
}

// publish espalha o estado atual: call-status para quem acompanha a chamada e
// call-list para quem reconcilia (mesmo par de eventos do wacalls).
func (c *Call) publish() {
	r := c.record()
	c.s.broker.emit(c.AccountID, map[string]any{
		"type": "call-status", "sessionId": r.SessionID, "id": r.CallID, "owner": r.Owner,
		"status": r.Status, "peer": r.Peer, "startedAt": r.StartedAt, "held": false,
		"extension": r.Extension, "agentEmail": r.AgentEmail,
	})
	c.s.broker.emit(c.AccountID, c.s.calls.listEvent(c.AccountID))
	c.wsEvent(map[string]any{"type": "call-status", "id": c.ID, "status": r.Status})
}

func (c *Call) wsEvent(ev map[string]any) {
	c.mu.Lock()
	ws := c.ws
	c.mu.Unlock()
	if ws != nil {
		ws.sendEvent(ev)
	}
}

// ---- mídia ----

// fromPBX: áudio que chegou do PABX vai para o consumidor atual.
func (c *Call) fromPBX(payload []byte, pt uint8) {
	c.mu.Lock()
	wr, ws, prev := c.webrtc, c.ws, c.lastSample
	if ws != nil && len(payload) > 0 {
		c.lastSample = decodeG711(payload[len(payload)-1:], pt)[0]
	}
	c.mu.Unlock()
	switch {
	case wr != nil:
		wr.writePCMU(toPCMU(payload, pt))
	case ws != nil:
		ws.writePCM(int16ToBytes(upsample8to16(decodeG711(payload, pt), prev)))
	}
}

// fromBrowser: o navegador já manda RTP ritmado, então vai direto para o PABX.
func (c *Call) fromBrowser(payload []byte) {
	c.leg.send(fromPCMU(payload, c.leg.codec()))
}

// feedPCM16: a IA manda em rajadas; o pacer é quem entrega ritmado.
func (c *Call) feedPCM16(data []byte) {
	c.mu.Lock()
	c.pcmBuf = append(c.pcmBuf, data...)
	if over := len(c.pcmBuf) - maxPCMBuffer; over > 0 {
		c.pcmBuf = c.pcmBuf[over+over%2:]
	}
	c.mu.Unlock()
}

// pace manda um quadro de 20 ms ao PABX sempre que o navegador NÃO é o consumidor:
// a fala da IA quando há, silêncio quando não há. Silêncio também conta — sem RTP
// fluindo, PABX costuma derrubar a chamada por "RTP timeout" e o NAT fecha.
func (c *Call) pace() {
	t := time.NewTicker(20 * time.Millisecond)
	defer t.Stop()
	silence := make([]int16, frameBytes8k)
	for {
		select {
		case <-c.stop:
			return
		case <-t.C:
		}
		c.mu.Lock()
		if c.status != statusConnected || c.webrtc != nil {
			c.mu.Unlock()
			continue
		}
		var frame []int16
		if len(c.pcmBuf) >= frameBytes16k {
			frame = downsample16to8(bytesToInt16(c.pcmBuf[:frameBytes16k]))
			c.pcmBuf = c.pcmBuf[frameBytes16k:]
		} else {
			frame = silence
		}
		c.mu.Unlock()
		c.leg.send(encodeG711(frame, c.leg.codec()))
	}
}

func (c *Call) attachWebRTC(leg *webrtcLeg) {
	c.mu.Lock()
	oldWR, oldWS := c.webrtc, c.ws
	c.webrtc, c.ws = leg, nil
	c.pcmBuf = nil
	c.mu.Unlock()
	if oldWR != nil {
		oldWR.replaced.Store(true)
		oldWR.close()
	}
	if oldWS != nil {
		oldWS.replaced.Store(true)
		oldWS.close()
	}
}

func (c *Call) attachWS(leg *wsLeg) {
	c.mu.Lock()
	oldWR, oldWS := c.webrtc, c.ws
	c.webrtc, c.ws = nil, leg
	c.pcmBuf = nil
	c.mu.Unlock()
	if oldWR != nil {
		oldWR.replaced.Store(true)
		oldWR.close()
	}
	if oldWS != nil {
		oldWS.replaced.Store(true)
		oldWS.close()
	}
}

// consumerGone: o consumidor saiu sem ser trocado. Durante uma transferência por
// REFER quem decide o fim é o PABX (NOTIFY), não o consumidor.
func (c *Call) consumerGone(reason string) {
	c.mu.Lock()
	transferring := c.transferring
	c.mu.Unlock()
	if transferring {
		return
	}
	c.hangup(reason)
}

// ---- ciclo de vida ----

func (c *Call) accept(owner string) error {
	c.mu.Lock()
	if c.status != statusRinging || c.srv == nil {
		c.mu.Unlock()
		return errNotRinging
	}
	srv := c.srv
	c.mu.Unlock()

	sdp := buildSDP(c.s.cfg.PublicIP, c.leg.localPort(), c.leg.codec())
	if err := srv.RespondSDP([]byte(sdp)); err != nil {
		return err
	}
	c.settle()
	c.mu.Lock()
	c.status = statusConnected
	c.owner = &owner
	if c.ringTimer != nil {
		c.ringTimer.Stop()
	}
	c.mu.Unlock()
	c.s.broker.emit(c.AccountID, map[string]any{
		"type": "incoming-claimed", "sessionId": c.LineID, "id": c.ID, "owner": owner,
	})
	c.publish()
	return nil
}

// connected: a saída foi atendida pelo PABX.
func (c *Call) connected() {
	c.mu.Lock()
	if c.ended {
		c.mu.Unlock()
		return
	}
	c.status = statusConnected
	c.mu.Unlock()
	c.publish()
}

func (c *Call) reject() error {
	c.mu.Lock()
	srv, status := c.srv, c.status
	c.mu.Unlock()
	if status != statusRinging || srv == nil {
		return errNotRinging
	}
	_ = srv.Respond(603, "Decline", nil)
	c.end("declined")
	return nil
}

// hangup encerra do nosso lado, conforme o estado: recusa se ainda toca na
// entrada, CANCEL se ainda chama na saída, BYE se já atendida.
func (c *Call) hangup(reason string) {
	c.mu.Lock()
	srv, cli, cancel, status := c.srv, c.cli, c.cancelDial, c.status
	c.mu.Unlock()
	ctx, done := context.WithTimeout(context.Background(), 5*time.Second)
	defer done()
	switch {
	case status == statusRinging && srv != nil:
		_ = srv.Respond(480, "Temporarily Unavailable", nil)
	case status == statusRinging && cancel != nil:
		cancel() // o WaitAnswer manda o CANCEL
	case srv != nil:
		_ = srv.Bye(ctx)
	case cli != nil:
		_ = cli.Bye(ctx)
	}
	c.end(reason)
}

// end é idempotente: libera tudo e avisa todo mundo.
func (c *Call) end(reason string) {
	c.mu.Lock()
	if c.ended {
		c.mu.Unlock()
		return
	}
	c.ended = true
	c.status = statusEnded
	if c.ringTimer != nil {
		c.ringTimer.Stop()
	}
	wr, ws, owner := c.webrtc, c.ws, c.owner
	c.webrtc, c.ws = nil, nil
	close(c.stop)
	c.mu.Unlock()

	if ws != nil {
		ws.sendEvent(map[string]any{"type": "call-ended", "id": c.ID, "reason": reason})
		ws.close()
	}
	if wr != nil {
		wr.close()
	}
	c.leg.close()
	c.settle()
	c.s.calls.remove(c)
	c.s.broker.emit(c.AccountID, map[string]any{
		"type": "call-ended", "sessionId": c.LineID, "id": c.ID, "owner": owner,
		"reason": reason, "endedAt": time.Now().UnixMilli(),
	})
	c.s.broker.emit(c.AccountID, c.s.calls.listEvent(c.AccountID))
	c.s.log.Info("chamada encerrada", "call", c.ID, "line", c.LineID, "reason", reason)
}

// callFor resolve {sid}/{id} da URL respeitando o escopo da credencial.
func (s *server) callFor(w http.ResponseWriter, r *http.Request, p principal) *Call {
	c := s.calls.get(r.PathValue("id"))
	if c == nil || c.LineID != r.PathValue("sid") || !p.canSee(c.AccountID) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such call"})
		return nil
	}
	return c
}

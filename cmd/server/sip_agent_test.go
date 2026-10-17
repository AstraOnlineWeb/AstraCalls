package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
	"github.com/pion/rtp"
)

func TestG711RoundTrip(t *testing.T) {
	for _, pt := range []uint8{rtpPTPCMU, rtpPTPCMA} {
		for _, v := range []int16{0, 100, -100, 1000, -1000, 8000, -8000, 30000, -30000} {
			got := g711Decode(pt, g711Encode(pt, v))
			diff := int(got) - int(v)
			if diff < 0 {
				diff = -diff
			}
			tol := int(v) / 16
			if tol < 0 {
				tol = -tol
			}
			if tol < 40 {
				tol = 40
			}
			if diff > tol {
				t.Fatalf("%s: %d virou %d (dif %d > %d)", g711Name(pt), v, got, diff, tol)
			}
		}
	}
}

func TestParseSDPOfferAndAnswer(t *testing.T) {
	offer := "v=0\r\nc=IN IP4 10.0.0.5\r\nm=audio 30000 RTP/AVP 8 0 101\r\na=rtpmap:8 PCMA/8000\r\na=rtpmap:101 telephone-event/8000\r\n"
	o := parseSDPOffer(offer)
	if o.addr != "10.0.0.5:30000" || o.dtmfPT != 101 {
		t.Fatalf("oferta mal lida: %+v", o)
	}
	pt, ok := o.pickG711()
	if !ok || pt != rtpPTPCMA {
		t.Fatalf("esperava PCMA primeiro, got %d %v", pt, ok)
	}
	ans := buildSIPSDPAnswer("1.2.3.4", 40000, pt, o.dtmfPT)
	for _, want := range []string{"m=audio 40000 RTP/AVP 8 101", "a=rtpmap:8 PCMA/8000", "a=rtpmap:101 telephone-event/8000", "a=ptime:20", "a=sendrecv"} {
		if !strings.Contains(ans, want) {
			t.Fatalf("answer sem %q:\n%s", want, ans)
		}
	}
	if _, ok := parseSDPOffer("m=audio 1 RTP/AVP 18\r\n").pickG711(); ok {
		t.Fatal("oferta só G.729 não deveria escolher G.711")
	}
}

func TestResamplersKeepLength(t *testing.T) {
	var d downsampler16to8
	total := 0
	for _, n := range []int{320, 7, 313, 1} { // blocos ímpares no meio
		total += len(d.process(make([]float32, n)))
	}
	if total != (320+7+313+1)/2 {
		t.Fatalf("downsample perdeu amostras: %d", total)
	}
	var u upsampler8to16
	if got := len(u.process(make([]float32, 160))); got != 320 {
		t.Fatalf("upsample: %d", got)
	}
}

func TestCallerIdentity(t *testing.T) {
	req := sip.NewRequest(sip.INVITE, sip.Uri{Scheme: "sip", User: "2001", Host: "pbx"})
	req.AppendHeader(&sip.FromHeader{DisplayName: "Fulano", Address: sip.Uri{Scheme: "sip", User: "+55 61 99999-8888", Host: "pbx"}, Params: sip.NewParams()})
	phone, name, _ := callerIdentity(req)
	if phone != "5561999998888" || name != "Fulano" {
		t.Fatalf("got %q %q", phone, name)
	}
	req.AppendHeader(sip.NewHeader("P-Asserted-Identity", `"X" <sip:5511988887777@pbx;user=phone>`))
	if phone, _, _ = callerIdentity(req); phone != "5511988887777" {
		t.Fatalf("PAI deveria ter prioridade, got %q", phone)
	}
}

func TestWantsAgentModes(t *testing.T) {
	s := &Session{SIPUser: "wa_ab12"}
	req := sip.NewRequest(sip.INVITE, sip.Uri{Scheme: "sip", User: "2001", Host: "pbx"})
	s.setSIPExt(sipExtConfig{User: "2001"})
	if wantsAgent(s, req) {
		t.Fatal("padrão (originate) não deveria ir pro agente")
	}
	s.setSIPExt(sipExtConfig{User: "2001", Agent: sipAgentOpts{InboundMode: "auto"}})
	if !wantsAgent(s, req) {
		t.Fatal("auto + ligaram pro próprio ramal => agente")
	}
	other := sip.NewRequest(sip.INVITE, sip.Uri{Scheme: "sip", User: "5561999998888", Host: "pbx"})
	if wantsAgent(s, other) {
		t.Fatal("auto + outro número => originate")
	}
	s.setSIPExt(sipExtConfig{User: "2001", Agent: sipAgentOpts{InboundMode: "agent"}})
	if !wantsAgent(s, other) {
		t.Fatal("agent => sempre agente")
	}
}

// TestSIPAgentEndToEnd liga de um UAC (PBX simulado) para o ramal em modo agente:
// 100/180, evento incoming, accept -> 200 com PCMA, ACK, RTP nos dois sentidos,
// DTMF, BYE do chamador -> call-ended. Tudo em 127.0.0.1.
func TestSIPAgentEndToEnd(t *testing.T) {
	t.Setenv("WACALLS_PUBLIC_IP", "127.0.0.1")
	t.Setenv("WACALLS_SIP_ADVERTISE_PORT", "15091")
	log := slog.Default()
	broker := NewBroker()
	mgr := &SessionManager{broker: broker, log: log, maxCalls: 8, sessions: map[string]*Session{}}
	sess := &Session{id: "s1", mgr: mgr, log: log, reg: newCallRegistry()}
	sess.setSIPExt(sipExtConfig{Enabled: true, Host: "127.0.0.1", User: "2001", Agent: sipAgentOpts{InboundMode: "agent"}})
	mgr.register(sess)

	gw, err := NewSIPGateway(mgr, log)
	if err != nil {
		t.Fatal(err)
	}
	mgr.sipAgentCount = gw.agentCount
	// registrador "fake" (sem loop): só para casar o IP de origem com o PBX.
	gw.extRegs["s1"] = &sipUACRegistrar{gw: gw, sessionID: "s1", cfg: sess.sipExtSnapshot()}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = gw.server.ListenAndServe(ctx, "udp", "127.0.0.1:15091") }()

	sub := broker.subscribe("t", 0)
	defer broker.unsubscribe(sub)

	// UAC (PBX simulado)
	ua, _ := sipgo.NewUA(sipgo.WithUserAgent("pbx-test"))
	cli, _ := sipgo.NewClient(ua, sipgo.WithClientHostname("127.0.0.1"), sipgo.WithClientPort(15092))
	srv, _ := sipgo.NewServer(ua)
	go func() { _ = srv.ListenAndServe(ctx, "udp", "127.0.0.1:15092") }()
	time.Sleep(200 * time.Millisecond)

	pbxRTP, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer pbxRTP.Close()
	offer := "v=0\r\no=pbx 1 1 IN IP4 127.0.0.1\r\ns=-\r\nc=IN IP4 127.0.0.1\r\nt=0 0\r\nm=audio " +
		itoa(pbxRTP.LocalAddr().(*net.UDPAddr).Port) + " RTP/AVP 8 101\r\na=rtpmap:8 PCMA/8000\r\na=rtpmap:101 telephone-event/8000\r\n"

	dua := &sipgo.DialogUA{Client: cli, ContactHDR: sip.ContactHeader{Address: sip.Uri{Scheme: "sip", User: "pbx", Host: "127.0.0.1", Port: 15092}}}
	from := &sip.FromHeader{DisplayName: "Fulano", Address: sip.Uri{Scheme: "sip", User: "5561999998888", Host: "127.0.0.1"}, Params: sip.NewParams()}
	from.Params.Add("tag", "pbxtag1")
	dlg, err := dua.Invite(ctx, sip.Uri{Scheme: "sip", User: "2001", Host: "127.0.0.1", Port: 15091}, []byte(offer), from, sip.NewHeader("Content-Type", "application/sdp"))
	if err != nil {
		t.Fatal(err)
	}

	// incoming no SSE com o número do From
	var callID string
	deadline := time.After(3 * time.Second)
	for callID == "" {
		select {
		case raw := <-sub.ch:
			var ev map[string]any
			_ = json.Unmarshal(raw, &ev)
			if ev["type"] == "incoming" {
				if ev["phone"] != "5561999998888" || ev["source"] != "sip" || ev["name"] != "Fulano" {
					t.Fatalf("incoming errado: %v", ev)
				}
				callID, _ = ev["id"].(string)
			}
		case <-deadline:
			t.Fatal("sem evento incoming")
		}
	}
	c := gw.agentCall("s1", callID)
	if c == nil || c.codec != "PCMA" {
		t.Fatalf("chamada não registrada ou codec errado: %+v", c)
	}
	if n := sess.activeCallCount(); n != 1 {
		t.Fatalf("activeCallCount = %d", n)
	}
	gotPCM := make(chan int, 10)
	gotDTMF := make(chan string, 2)
	c.rtp.SetHandlers(func(p []float32) { gotPCM <- len(p) }, func(d string) { gotDTMF <- d })

	if err := c.answer(); err != nil {
		t.Fatal(err)
	}
	wctx, wcancel := context.WithTimeout(ctx, 3*time.Second)
	defer wcancel()
	if err := dlg.WaitAnswer(wctx, sipgo.AnswerOptions{}); err != nil {
		t.Fatalf("sem 200 OK: %v", err)
	}
	ans := parseSDPOffer(string(dlg.InviteResponse.Body()))
	if pt, _ := ans.pickG711(); pt != rtpPTPCMA || ans.dtmfPT != 101 {
		t.Fatalf("answer sem PCMA/101: %s", dlg.InviteResponse.Body())
	}
	if err := dlg.Ack(ctx); err != nil {
		t.Fatal(err)
	}

	// PBX -> agente: 1 pacote PCMA de 20 ms vira 320 amostras a 16 kHz
	dst, _ := net.ResolveUDPAddr("udp", ans.addr)
	pkt := rtp.Packet{Header: rtp.Header{Version: 2, PayloadType: 8, SequenceNumber: 1, Timestamp: 160, SSRC: 9}, Payload: make([]byte, 160)}
	raw, _ := pkt.Marshal()
	_, _ = pbxRTP.WriteToUDP(raw, dst)
	select {
	case n := <-gotPCM:
		if n != 320 {
			t.Fatalf("esperava 320 amostras, got %d", n)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("áudio do PBX não chegou")
	}
	// DTMF "5" (repetido 3x com o mesmo timestamp => 1 dígito)
	for i := 0; i < 3; i++ {
		d := rtp.Packet{Header: rtp.Header{Version: 2, PayloadType: 101, SequenceNumber: uint16(2 + i), Timestamp: 800, SSRC: 9}, Payload: []byte{5, 0x0a, 0, 160}}
		raw, _ := d.Marshal()
		_, _ = pbxRTP.WriteToUDP(raw, dst)
	}
	select {
	case d := <-gotDTMF:
		if d != "5" {
			t.Fatalf("dtmf %q", d)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("dtmf não chegou")
	}
	select {
	case d := <-gotDTMF:
		t.Fatalf("dtmf duplicado %q", d)
	case <-time.After(200 * time.Millisecond):
	}

	// agente -> PBX: rajada de 100 ms vira pacotes PCMA de 160 bytes ritmados
	_ = c.rtp.WritePCM(make([]float32, 1600))
	_ = pbxRTP.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 1500)
	n, _, err := pbxRTP.ReadFromUDP(buf)
	if err != nil {
		t.Fatalf("sem RTP do agente: %v", err)
	}
	var in rtp.Packet
	if err := in.Unmarshal(buf[:n]); err != nil || in.PayloadType != 8 || len(in.Payload) != 160 {
		t.Fatalf("pacote errado: pt=%d len=%d err=%v", in.PayloadType, len(in.Payload), err)
	}

	// chamador desliga
	if err := dlg.Bye(ctx); err != nil {
		t.Fatalf("BYE: %v", err)
	}
	deadline = time.After(3 * time.Second)
	for {
		select {
		case raw := <-sub.ch:
			var ev map[string]any
			_ = json.Unmarshal(raw, &ev)
			if ev["type"] == "call-ended" && ev["id"] == callID {
				if ev["reason"] != "remote_hangup" {
					t.Fatalf("reason %v", ev["reason"])
				}
				if gw.agentCall("s1", callID) != nil {
					t.Fatal("chamada continua registrada")
				}
				return
			}
		case <-deadline:
			t.Fatal("sem call-ended")
		}
	}
}

type agentHarness struct {
	gw   *SIPGateway
	sess *Session
	sub  *subscriber
	dua  *sipgo.DialogUA
	ctx  context.Context
	bye  chan struct{} // BYE recebido pelo PBX simulado
}

func newAgentHarness(t *testing.T, sipPort, pbxPort int, opts sipAgentOpts) *agentHarness {
	t.Helper()
	t.Setenv("WACALLS_PUBLIC_IP", "127.0.0.1")
	t.Setenv("WACALLS_SIP_ADVERTISE_PORT", itoa(sipPort))
	log := slog.Default()
	broker := NewBroker()
	mgr := &SessionManager{broker: broker, log: log, maxCalls: 8, sessions: map[string]*Session{}}
	sess := &Session{id: "s1", mgr: mgr, log: log, reg: newCallRegistry()}
	sess.setSIPExt(sipExtConfig{Enabled: true, Host: "127.0.0.1", User: "2001", Agent: opts})
	mgr.register(sess)
	gw, err := NewSIPGateway(mgr, log)
	if err != nil {
		t.Fatal(err)
	}
	mgr.sipAgentCount = gw.agentCount
	gw.extRegs["s1"] = &sipUACRegistrar{gw: gw, sessionID: "s1", cfg: sess.sipExtSnapshot()}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = gw.server.ListenAndServe(ctx, "udp", "127.0.0.1:"+itoa(sipPort)) }()
	sub := broker.subscribe("t", 0)
	t.Cleanup(func() { broker.unsubscribe(sub) })
	ua, _ := sipgo.NewUA()
	cli, _ := sipgo.NewClient(ua, sipgo.WithClientHostname("127.0.0.1"), sipgo.WithClientPort(pbxPort))
	srv, _ := sipgo.NewServer(ua)
	bye := make(chan struct{}, 1)
	srv.OnBye(func(req *sip.Request, tx sip.ServerTransaction) {
		_ = tx.Respond(sip.NewResponseFromRequest(req, 200, "OK", nil))
		bye <- struct{}{}
	})
	go func() { _ = srv.ListenAndServe(ctx, "udp", "127.0.0.1:"+itoa(pbxPort)) }()
	time.Sleep(200 * time.Millisecond)
	dua := &sipgo.DialogUA{Client: cli, ContactHDR: sip.ContactHeader{Address: sip.Uri{Scheme: "sip", User: "pbx", Host: "127.0.0.1", Port: pbxPort}}}
	return &agentHarness{gw: gw, sess: sess, sub: sub, dua: dua, ctx: ctx, bye: bye}
}

func (h *agentHarness) invite(t *testing.T, sipPort int) *sipgo.DialogClientSession {
	t.Helper()
	offer := "v=0\r\nc=IN IP4 127.0.0.1\r\nt=0 0\r\nm=audio 30000 RTP/AVP 0\r\n"
	from := &sip.FromHeader{Address: sip.Uri{Scheme: "sip", User: "5561999998888", Host: "127.0.0.1"}, Params: sip.NewParams()}
	from.Params.Add("tag", generateRandomString(8))
	d, err := h.dua.Invite(h.ctx, sip.Uri{Scheme: "sip", User: "2001", Host: "127.0.0.1", Port: sipPort}, []byte(offer), from, sip.NewHeader("Content-Type", "application/sdp"))
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func (h *agentHarness) waitEvent(t *testing.T, typ string) map[string]any {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case raw := <-h.sub.ch:
			var ev map[string]any
			_ = json.Unmarshal(raw, &ev)
			if ev["type"] == typ {
				return ev
			}
		case <-deadline:
			t.Fatalf("sem evento %s", typ)
		}
	}
}

func finalCode(t *testing.T, ctx context.Context, d *sipgo.DialogClientSession) int {
	t.Helper()
	wctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	err := d.WaitAnswer(wctx, sipgo.AnswerOptions{})
	var er *sipgo.ErrDialogResponse
	if errors.As(err, &er) {
		return er.Res.StatusCode
	}
	if err == nil {
		return 200
	}
	t.Fatalf("WaitAnswer: %v", err)
	return 0
}

func TestSIPAgentRingTimeout480(t *testing.T) {
	h := newAgentHarness(t, 15093, 15094, sipAgentOpts{InboundMode: "agent", RingTimeout: 1})
	d := h.invite(t, 15093)
	h.waitEvent(t, "incoming")
	if code := finalCode(t, h.ctx, d); code != 480 {
		t.Fatalf("esperava 480, got %d", code)
	}
	if ev := h.waitEvent(t, "call-ended"); ev["reason"] != "timeout" {
		t.Fatalf("reason %v", ev["reason"])
	}
}

func TestSIPAgentReject486(t *testing.T) {
	h := newAgentHarness(t, 15095, 15096, sipAgentOpts{InboundMode: "agent"})
	d := h.invite(t, 15095)
	ev := h.waitEvent(t, "incoming")
	h.gw.agentCall("s1", ev["id"].(string)).end("declined", 486)
	if code := finalCode(t, h.ctx, d); code != 486 {
		t.Fatalf("esperava 486, got %d", code)
	}
}

func TestSIPAgentAutoAnswerAndAgentHangup(t *testing.T) {
	h := newAgentHarness(t, 15097, 15098, sipAgentOpts{InboundMode: "agent", AutoAnswer: true})
	d := h.invite(t, 15097)
	ev := h.waitEvent(t, "incoming")
	if code := finalCode(t, h.ctx, d); code != 200 {
		t.Fatalf("auto-answer deveria dar 200, got %d", code)
	}
	if err := d.Ack(h.ctx); err != nil {
		t.Fatal(err)
	}
	h.gw.agentCall("s1", ev["id"].(string)).end("agent_hangup", 486)
	if ev := h.waitEvent(t, "call-ended"); ev["reason"] != "agent_hangup" {
		t.Fatalf("reason %v", ev["reason"])
	}
	select {
	case <-h.bye:
	case <-time.After(3 * time.Second):
		t.Fatal("PBX não recebeu BYE")
	}
}

func TestSIPAgentCallerCancel(t *testing.T) {
	h := newAgentHarness(t, 15099, 15100, sipAgentOpts{InboundMode: "agent"})
	d := h.invite(t, 15099)
	h.waitEvent(t, "incoming")
	// ctx curto: o WaitAnswer manda CANCEL ao expirar (chamador desistiu)
	wctx, cancel := context.WithTimeout(h.ctx, 300*time.Millisecond)
	defer cancel()
	_ = d.WaitAnswer(wctx, sipgo.AnswerOptions{})
	if ev := h.waitEvent(t, "call-ended"); ev["reason"] != "caller_cancelled" {
		t.Fatalf("reason %v", ev["reason"])
	}
}

func TestSIPAgentWSContract(t *testing.T) {
	h := newAgentHarness(t, 15101, 15102, sipAgentOpts{InboundMode: "agent"})
	pbxRTP, _ := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	defer pbxRTP.Close()
	offer := "v=0\r\nc=IN IP4 127.0.0.1\r\nt=0 0\r\nm=audio " + itoa(pbxRTP.LocalAddr().(*net.UDPAddr).Port) + " RTP/AVP 0\r\n"
	from := &sip.FromHeader{Address: sip.Uri{Scheme: "sip", User: "5561999998888", Host: "127.0.0.1"}, Params: sip.NewParams()}
	from.Params.Add("tag", "ws1")
	d, err := h.dua.Invite(h.ctx, sip.Uri{Scheme: "sip", User: "2001", Host: "127.0.0.1", Port: 15101}, []byte(offer), from, sip.NewHeader("Content-Type", "application/sdp"))
	if err != nil {
		t.Fatal(err)
	}
	ev := h.waitEvent(t, "incoming")
	c := h.gw.agentCall("s1", ev["id"].(string))
	_ = c.answer()
	if code := finalCode(t, h.ctx, d); code != 200 {
		t.Fatalf("got %d", code)
	}
	_ = d.Ack(h.ctx)

	srv := &server{log: slog.Default()}
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { srv.sipAgentWS(c, w, r) }))
	defer hs.Close()
	ws, _, err := websocket.Dial(h.ctx, "ws"+strings.TrimPrefix(hs.URL, "http")+"/?events=1", &websocket.DialOptions{Subprotocols: []string{"pcm16"}})
	if err != nil {
		t.Fatal(err)
	}
	rctx, rcancel := context.WithTimeout(h.ctx, 3*time.Second)
	defer rcancel()
	mt, data, err := ws.Read(rctx)
	if err != nil || mt != websocket.MessageText || !strings.Contains(string(data), "connected") {
		t.Fatalf("esperava call-status connected, got %v %s %v", mt, data, err)
	}

	// agente -> PBX (PCM16 LE 16 kHz, 20 ms)
	if err := ws.Write(h.ctx, websocket.MessageBinary, make([]byte, 640)); err != nil {
		t.Fatal(err)
	}
	_ = pbxRTP.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 1500)
	n, src, err := pbxRTP.ReadFromUDP(buf)
	if err != nil {
		t.Fatalf("RTP do agente não chegou: %v", err)
	}
	var in rtp.Packet
	if in.Unmarshal(buf[:n]) != nil || in.PayloadType != 0 || len(in.Payload) != 160 {
		t.Fatalf("pacote errado pt=%d len=%d", in.PayloadType, len(in.Payload))
	}

	// PBX -> agente: 20 ms PCMU vira 640 bytes PCM16 16 kHz no WS
	pkt := rtp.Packet{Header: rtp.Header{Version: 2, PayloadType: 0, SequenceNumber: 1, Timestamp: 160, SSRC: 7}, Payload: make([]byte, 160)}
	raw, _ := pkt.Marshal()
	_, _ = pbxRTP.WriteToUDP(raw, src)
	for {
		mt, data, err = ws.Read(rctx)
		if err != nil {
			t.Fatalf("sem áudio no WS: %v", err)
		}
		if mt == websocket.MessageBinary {
			if len(data) != 640 {
				t.Fatalf("esperava 640 bytes, got %d", len(data))
			}
			break
		}
	}

	// clear esvazia a fila de saída
	_ = c.rtp.WritePCM(make([]float32, 16000))
	_ = ws.Write(h.ctx, websocket.MessageText, []byte(`{"type":"clear"}`))
	time.Sleep(100 * time.Millisecond)
	c.rtp.mu.Lock()
	queued := len(c.rtp.outQueue)
	c.rtp.mu.Unlock()
	if queued != 0 {
		t.Fatalf("clear não esvaziou a fila: %d", queued)
	}

	// fechar o WS = agente desligou -> BYE + call-ended
	_ = ws.Close(websocket.StatusNormalClosure, "")
	if ev := h.waitEvent(t, "call-ended"); ev["reason"] != "agent_hangup" {
		t.Fatalf("reason %v", ev["reason"])
	}
	select {
	case <-h.bye:
	case <-time.After(3 * time.Second):
		t.Fatal("PBX não recebeu BYE")
	}
}

// TestSIPAgentTransfer: IA atende pelo WS, transfere (espera com música, WS da IA
// solto sem derrubar a ligação), operador assume com /pickup e conecta o áudio.
func TestSIPAgentTransfer(t *testing.T) {
	h := newAgentHarness(t, 15103, 15104, sipAgentOpts{InboundMode: "agent"})
	pbxRTP, _ := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	defer pbxRTP.Close()
	offer := "v=0\r\nc=IN IP4 127.0.0.1\r\nt=0 0\r\nm=audio " + itoa(pbxRTP.LocalAddr().(*net.UDPAddr).Port) + " RTP/AVP 0\r\n"
	from := &sip.FromHeader{Address: sip.Uri{Scheme: "sip", User: "5561999998888", Host: "127.0.0.1"}, Params: sip.NewParams()}
	from.Params.Add("tag", "tr1")
	d, err := h.dua.Invite(h.ctx, sip.Uri{Scheme: "sip", User: "2001", Host: "127.0.0.1", Port: 15103}, []byte(offer), from, sip.NewHeader("Content-Type", "application/sdp"))
	if err != nil {
		t.Fatal(err)
	}
	ev := h.waitEvent(t, "incoming")
	id := ev["id"].(string)
	c := h.gw.agentCall("s1", id)
	b := h.gw.sessions.broker
	srv := &server{log: slog.Default(), broker: b}

	// a ponte da IA aceita (vira dona)
	req := httptest.NewRequest("POST", "/", nil)
	req.Header.Set("X-Client-Id", "bridge")
	rec := httptest.NewRecorder()
	srv.sipAgentAccept(c, rec, req)
	if rec.Code != 200 {
		t.Fatalf("accept %d", rec.Code)
	}
	if ev := h.waitEvent(t, "incoming-claimed"); ev["owner"] != "bridge" {
		t.Fatalf("claimed %v", ev)
	}
	if code := finalCode(t, h.ctx, d); code != 200 {
		t.Fatalf("got %d", code)
	}
	_ = d.Ack(h.ctx)

	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { srv.sipAgentWS(c, w, r) }))
	defer hs.Close()
	wsURL := "ws" + strings.TrimPrefix(hs.URL, "http") + "/?events=1"
	ai, _, err := websocket.Dial(h.ctx, wsURL, &websocket.DialOptions{Subprotocols: []string{"pcm16"}})
	if err != nil {
		t.Fatal(err)
	}
	rctx, rcancel := context.WithTimeout(h.ctx, 3*time.Second)
	defer rcancel()
	_, _, _ = ai.Read(rctx) // call-status connected

	// operador sem posse ainda: /accept dá 409
	req = httptest.NewRequest("POST", "/", nil)
	req.Header.Set("X-Client-Id", "op1")
	rec = httptest.NewRecorder()
	srv.sipAgentAccept(c, rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("accept do operador antes da transferência deveria dar 409, got %d", rec.Code)
	}

	// IA transfere
	req = httptest.NewRequest("POST", "/", strings.NewReader(`{"to":"op1"}`))
	req.Header.Set("X-Client-Id", "bridge")
	rec = httptest.NewRecorder()
	srv.sipAgentTransfer(c, rec, req)
	if rec.Code != 200 {
		t.Fatalf("transfer %d %s", rec.Code, rec.Body)
	}
	_, data, err := ai.Read(rctx)
	if err != nil || !strings.Contains(string(data), "call-transferred") {
		t.Fatalf("IA deveria receber call-transferred: %s %v", data, err)
	}
	if _, _, err := ai.Read(rctx); err == nil {
		t.Fatal("WS da IA deveria fechar")
	}
	if cr, _ := b.getCall(id); cr == nil || !cr.Held || cr.Owner != nil {
		t.Fatalf("esperava em espera e sem dono: %+v", cr)
	}

	// quem ligou ouve música de espera (não silêncio) e a ligação NÃO cai
	_ = pbxRTP.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 1500)
	heard := false
	for i := 0; i < 50 && !heard; i++ {
		n, _, err := pbxRTP.ReadFromUDP(buf)
		if err != nil {
			t.Fatalf("sem RTP em espera: %v", err)
		}
		var p rtp.Packet
		if p.Unmarshal(buf[:n]) == nil {
			for _, x := range p.Payload {
				if x != 0xFF && x != 0x7F {
					heard = true
					break
				}
			}
		}
	}
	if !heard {
		t.Fatal("em espera só chegou silêncio")
	}
	select {
	case <-h.bye:
		t.Fatal("transferência não pode mandar BYE")
	default:
	}
	if h.gw.agentCall("s1", id) == nil {
		t.Fatal("chamada sumiu após a transferência")
	}

	// operador assume e conecta o áudio (WS); sai da espera
	req = httptest.NewRequest("POST", "/", nil)
	req.Header.Set("X-Client-Id", "op1")
	rec = httptest.NewRecorder()
	srv.sipAgentPickup(c, rec, req)
	if rec.Code != 200 {
		t.Fatalf("pickup %d", rec.Code)
	}
	if ev := h.waitEvent(t, "call-transfer-claimed"); ev["owner"] != "op1" {
		t.Fatalf("transfer-claimed %v", ev)
	}
	op, _, err := websocket.Dial(h.ctx, wsURL, &websocket.DialOptions{Subprotocols: []string{"pcm16"}})
	if err != nil {
		t.Fatal(err)
	}
	_, _, _ = op.Read(rctx)
	if cr, _ := b.getCall(id); cr == nil || cr.Held || cr.Owner == nil || *cr.Owner != "op1" {
		t.Fatalf("esperava op1 dono e fora da espera: %+v", cr)
	}
	c.end("agent_hangup", 486)
	select {
	case <-h.bye:
	case <-time.After(3 * time.Second):
		t.Fatal("PBX não recebeu BYE")
	}
}

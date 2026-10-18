package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
	"github.com/pion/rtp"
)

// Ponta a ponta contra um PABX falso (sipgo) em loopback: o conector registra como
// ramal, recebe uma ligação, a "IA" fala (feedPCM16 -> RTP no PABX), o PABX desliga;
// depois o conector disca para o PABX e desliga do nosso lado.

func freeUDPPort(t *testing.T) int {
	t.Helper()
	c, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	return c.LocalAddr().(*net.UDPAddr).Port
}

type fakePBX struct {
	port       int
	dialog     *sipgo.DialogUA
	mu         sync.Mutex
	registered chan struct{}
	srvDialog  *sipgo.DialogServerSession
	cliDialog  *sipgo.DialogClientSession
	rtp        *net.UDPConn
	onRefer    func(*sip.Request, sip.ServerTransaction)

	// PABX estilo 3CX: o ramal (AOR) autentica com OUTRO login (ID de autenticação).
	// ramal -> {login, senha}; ramal fora do mapa entra sem senha.
	creds     map[string][2]string
	nonces    *nonceStore
	authUsers []string // logins que autenticaram com sucesso
}

// exigeDigest devolve true se o pedido pode seguir; senão já respondeu 401.
func (p *fakePBX) exigeDigest(req *sip.Request, tx sip.ServerTransaction, ramal string) bool {
	p.mu.Lock()
	cred, ok := p.creds[ramal]
	p.mu.Unlock()
	if !ok {
		return true
	}
	if h := req.GetHeader("Authorization"); h != nil &&
		p.nonces.checkDigest(h.Value(), string(req.Method), cred[0], cred[1]) {
		p.mu.Lock()
		p.authUsers = append(p.authUsers, parseDigestHeader(h.Value())["username"])
		p.mu.Unlock()
		return true
	}
	res := sip.NewResponseFromRequest(req, 401, "Unauthorized", nil)
	res.AppendHeader(sip.NewHeader("WWW-Authenticate", p.nonces.challenge()))
	_ = tx.Respond(res)
	return false
}

func startFakePBX(t *testing.T, ctx context.Context) *fakePBX {
	t.Helper()
	p := &fakePBX{port: freeUDPPort(t), registered: make(chan struct{}, 4), nonces: newNonceStore()}
	ua, _ := sipgo.NewUA(sipgo.WithUserAgent("fake-pbx"))
	srv, _ := sipgo.NewServer(ua)
	cli, _ := sipgo.NewClient(ua, sipgo.WithClientHostname("127.0.0.1"))
	p.dialog = &sipgo.DialogUA{Client: cli, ContactHDR: sip.ContactHeader{
		Address: sip.Uri{Scheme: "sip", User: "pbx", Host: "127.0.0.1", Port: p.port},
	}}
	var err error
	if p.rtp, err = net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)}); err != nil {
		t.Fatal(err)
	}
	srv.OnRegister(func(req *sip.Request, tx sip.ServerTransaction) {
		if !p.exigeDigest(req, tx, req.From().Address.User) {
			return
		}
		res := sip.NewResponseFromRequest(req, 200, "OK", nil)
		res.AppendHeader(sip.NewHeader("Expires", "300"))
		_ = tx.Respond(res)
		select {
		case p.registered <- struct{}{}:
		default:
		}
	})
	// ligação que o conector faz PARA o PABX: atende na hora
	srv.OnInvite(func(req *sip.Request, tx sip.ServerTransaction) {
		if !p.exigeDigest(req, tx, req.From().Address.User) {
			return
		}
		d, err := p.dialog.ReadInvite(req, tx)
		if err != nil {
			return
		}
		p.mu.Lock()
		p.srvDialog = d
		p.mu.Unlock()
		_ = d.Respond(180, "Ringing", nil)
		_ = d.RespondSDP([]byte(buildSDP("127.0.0.1", p.rtpPort(), ptPCMA)))
		<-d.Context().Done()
	})
	srv.OnAck(func(req *sip.Request, tx sip.ServerTransaction) {
		p.mu.Lock()
		d := p.srvDialog
		p.mu.Unlock()
		if d != nil {
			_ = d.ReadAck(req, tx)
		}
	})
	srv.OnBye(func(req *sip.Request, tx sip.ServerTransaction) {
		p.mu.Lock()
		sd, cd := p.srvDialog, p.cliDialog
		p.mu.Unlock()
		if sd != nil {
			_ = sd.ReadBye(req, tx)
		} else if cd != nil {
			_ = cd.ReadBye(req, tx)
		}
	})
	srv.OnRefer(func(req *sip.Request, tx sip.ServerTransaction) {
		if p.onRefer != nil {
			p.onRefer(req, tx)
		}
	})
	go func() { _ = srv.ListenAndServe(ctx, "udp", net.JoinHostPort("127.0.0.1", strconv.Itoa(p.port))) }()
	t.Cleanup(func() { p.rtp.Close() })
	return p
}

func (p *fakePBX) rtpPort() int { return p.rtp.LocalAddr().(*net.UDPAddr).Port }

// awaitRTP espera um pacote RTP do conector com o payload type esperado.
func (p *fakePBX) awaitRTP(t *testing.T, pt uint8) {
	t.Helper()
	buf := make([]byte, 1500)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		_ = p.rtp.SetReadDeadline(deadline)
		n, _, err := p.rtp.ReadFromUDP(buf)
		if err != nil {
			break
		}
		var pkt rtp.Packet
		if pkt.Unmarshal(buf[:n]) == nil && pkt.PayloadType == pt && len(pkt.Payload) == frameBytes8k {
			return
		}
	}
	t.Fatalf("nenhum RTP pt=%d chegou ao PABX", pt)
}

func startConnector(t *testing.T, ctx context.Context) *server {
	t.Helper()
	port := freeUDPPort(t)
	cfg := config{
		SIPAddr: net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), APIKey: "k", PublicIP: "127.0.0.1",
		SIPAdvertisePort: port, RTPMin: 41000, RTPMax: 41099, DataDir: t.TempDir(), RingTimeout: 5 * time.Second,
	}
	st, err := openStore(cfg.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	s := &server{cfg: cfg, log: slog.New(slog.NewTextHandler(os.Stderr, nil)), store: st,
		broker: newBroker(), calls: newCallManager(), ports: newPortAllocator(cfg.RTPMin, cfg.RTPMax)}
	if s.sip, err = newSipUA(s); err != nil {
		t.Fatal(err)
	}
	s.sip.start(ctx)
	time.Sleep(100 * time.Millisecond)
	return s
}

func awaitEvent(t *testing.T, sub *subscriber, typ string) map[string]any {
	t.Helper()
	timeout := time.After(5 * time.Second)
	for {
		select {
		case data := <-sub.ch:
			var ev map[string]any
			_ = json.Unmarshal(data, &ev)
			if ev["type"] == typ {
				return ev
			}
		case <-timeout:
			t.Fatalf("evento %q não chegou", typ)
		}
	}
}

func TestEndToEndWithFakePBX(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pbx := startFakePBX(t, ctx)
	s := startConnector(t, ctx)

	line := Line{ID: "inbox16", AccountID: 1, InboxID: 16, Enabled: true, Mode: modeRegisterOut,
		Host: "127.0.0.1", Port: pbx.port, User: "100", Password: "x",
		Agents: []Agent{{Email: "agente@botfy", Extension: "201", Password: "y"}}}
	if err := line.normalize(nil); err != nil {
		t.Fatal(err)
	}
	if err := s.store.put(line); err != nil {
		t.Fatal(err)
	}
	s.sip.applyLine(line)

	// linha + ramal do agente registrados no PABX
	for i := 0; i < 2; i++ {
		select {
		case <-pbx.registered:
		case <-time.After(5 * time.Second):
			t.Fatal("o conector não registrou no PABX")
		}
	}
	time.Sleep(100 * time.Millisecond)
	st, agents := s.sip.lineStatus(line)
	if st.State != regRegistered || agents["201"].State != regRegistered {
		t.Fatalf("status: %+v %+v", st, agents)
	}

	sub := s.broker.subscribe("teste", 1)
	outraConta := s.broker.subscribe("intruso", 2)

	t.Run("entrada para o ramal do agente", func(t *testing.T) {
		from := &sip.FromHeader{DisplayName: "Cliente", Address: sip.Uri{Scheme: "sip", User: "5561999990000", Host: "127.0.0.1"}, Params: sip.NewParams()}
		from.Params.Add("tag", generateID(8))
		target := sip.Uri{Scheme: "sip", User: contactUserFor("inbox16", "201"), Host: "127.0.0.1", Port: s.cfg.SIPAdvertisePort}
		d, err := pbx.dialog.Invite(ctx, target, []byte(buildSDP("127.0.0.1", pbx.rtpPort(), ptPCMU, ptPCMA)),
			from, sip.NewHeader("Content-Type", "application/sdp"))
		if err != nil {
			t.Fatal(err)
		}
		pbx.mu.Lock()
		pbx.cliDialog = d
		pbx.mu.Unlock()

		ev := awaitEvent(t, sub, "incoming")
		if ev["phone"] != "5561999990000" || ev["agentEmail"] != "agente@botfy" || ev["sessionId"] != "inbox16" {
			t.Fatalf("incoming: %+v", ev)
		}
		select {
		case data := <-outraConta.ch:
			t.Fatalf("evento vazou para outra conta: %s", data)
		default:
		}
		c := s.calls.get(ev["id"].(string))

		accepted := make(chan error, 1)
		go func() { accepted <- c.accept("tester") }()
		if err := d.WaitAnswer(ctx, sipgo.AnswerOptions{}); err != nil {
			t.Fatal(err)
		}
		if err := d.Ack(ctx); err != nil {
			t.Fatal(err)
		}
		if err := <-accepted; err != nil {
			t.Fatal(err)
		}
		awaitEvent(t, sub, "incoming-claimed")

		// a IA fala: PCM16 16 kHz entra, PCMU 20 ms sai ritmado para o PABX
		c.feedPCM16(make([]byte, frameBytes16k*10))
		pbx.awaitRTP(t, ptPCMU)

		bctx, done := context.WithTimeout(ctx, 5*time.Second)
		defer done()
		if err := d.Bye(bctx); err != nil {
			t.Fatal(err)
		}
		end := awaitEvent(t, sub, "call-ended")
		if end["reason"] != "remote_hangup" || s.calls.get(c.ID) != nil {
			t.Fatalf("fim: %+v", end)
		}
		pbx.mu.Lock()
		pbx.cliDialog = nil
		pbx.mu.Unlock()
	})

	t.Run("saída pelo ramal do agente e desligar do nosso lado", func(t *testing.T) {
		c, err := s.sip.dial(line, line.agentByEmail("agente@botfy"), "5561988887777", "tester")
		if err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(5 * time.Second)
		for c.currentStatus() != statusConnected && time.Now().Before(deadline) {
			time.Sleep(50 * time.Millisecond)
		}
		if c.currentStatus() != statusConnected {
			t.Fatal("a saída não conectou")
		}
		if c.leg.codec() != ptPCMA {
			t.Fatal("deveria usar o PCMA que o PABX respondeu")
		}
		c.feedPCM16(make([]byte, frameBytes16k*10))
		pbx.awaitRTP(t, ptPCMA)

		c.hangup("user_ended")
		end := awaitEvent(t, sub, "call-ended")
		if end["reason"] != "user_ended" {
			t.Fatalf("fim: %+v", end)
		}
	})

	t.Run("IA pelo WS pcm16: ouve, fala e desligar o WS derruba a ligação", func(t *testing.T) {
		from := &sip.FromHeader{Address: sip.Uri{Scheme: "sip", User: "5561977776666", Host: "127.0.0.1"}, Params: sip.NewParams()}
		from.Params.Add("tag", generateID(8))
		target := sip.Uri{Scheme: "sip", User: contactUserFor("inbox16", ""), Host: "127.0.0.1", Port: s.cfg.SIPAdvertisePort}
		d, err := pbx.dialog.Invite(ctx, target, []byte(buildSDP("127.0.0.1", pbx.rtpPort(), ptPCMU)),
			from, sip.NewHeader("Content-Type", "application/sdp"))
		if err != nil {
			t.Fatal(err)
		}
		pbx.mu.Lock()
		pbx.srvDialog, pbx.cliDialog = nil, d
		pbx.mu.Unlock()
		ev := awaitEvent(t, sub, "incoming")
		if ev["agentEmail"] != nil && ev["agentEmail"] != "" {
			t.Fatalf("chamada da linha não deveria ter agente: %+v", ev)
		}
		c := s.calls.get(ev["id"].(string))
		go func() { _ = c.accept("ponte-de-voz") }()
		if err := d.WaitAnswer(ctx, sipgo.AnswerOptions{}); err != nil {
			t.Fatal(err)
		}
		_ = d.Ack(ctx)

		api := httptest.NewServer(s.routes())
		defer api.Close()
		wsURL := "ws" + strings.TrimPrefix(api.URL, "http") + "/api/sessions/inbox16/calls/" + c.ID + "/ws?apiKey=k&events=1"
		conn, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{Subprotocols: []string{"pcm16"}})
		if err != nil {
			t.Fatal(err)
		}
		time.Sleep(50 * time.Millisecond)

		// PABX -> IA: um quadro PCMU de 20 ms vira 640 bytes de PCM16 16 kHz
		pkt := rtp.Packet{Header: rtp.Header{Version: 2, PayloadType: ptPCMU, SequenceNumber: 1, SSRC: 9},
			Payload: make([]byte, frameBytes8k)}
		raw, _ := pkt.Marshal()
		legAddr := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: c.leg.localPort()}
		got := false
		for i := 0; i < 20 && !got; i++ {
			_, _ = pbx.rtp.WriteToUDP(raw, legAddr)
			rctx, done := context.WithTimeout(ctx, 200*time.Millisecond)
			mt, data, err := conn.Read(rctx)
			done()
			got = err == nil && mt == websocket.MessageBinary && len(data) == frameBytes16k
		}
		if !got {
			t.Fatal("a IA não recebeu o áudio do PABX")
		}

		// IA -> PABX
		if err := conn.Write(ctx, websocket.MessageBinary, make([]byte, frameBytes16k*5)); err != nil {
			t.Fatal(err)
		}
		pbx.awaitRTP(t, ptPCMU)

		_ = conn.Close(websocket.StatusNormalClosure, "fim")
		end := awaitEvent(t, sub, "call-ended")
		if end["reason"] != "ws-closed" {
			t.Fatalf("fim: %+v", end)
		}
		pbx.mu.Lock()
		pbx.cliDialog = nil
		pbx.mu.Unlock()
	})

	t.Run("transferência para ramal por REFER", func(t *testing.T) {
		referred := make(chan string, 1)
		pbx.onRefer = func(req *sip.Request, tx sip.ServerTransaction) {
			_ = tx.Respond(sip.NewResponseFromRequest(req, 202, "Accepted", nil))
			referred <- req.GetHeader("Refer-To").Value()
			// o PABX conclui a transferência e avisa com NOTIFY (sipfrag 200)
			go func() {
				pbx.mu.Lock()
				d := pbx.cliDialog
				pbx.mu.Unlock()
				n := sip.NewRequest(sip.NOTIFY, d.InviteResponse.Contact().Address)
				n.AppendHeader(sip.NewHeader("Event", "refer"))
				n.AppendHeader(sip.NewHeader("Content-Type", "message/sipfrag"))
				n.SetBody([]byte("SIP/2.0 200 OK"))
				nctx, done := context.WithTimeout(ctx, 5*time.Second)
				defer done()
				_, _ = d.Do(nctx, n)
			}()
		}
		from := &sip.FromHeader{Address: sip.Uri{Scheme: "sip", User: "5561955554444", Host: "127.0.0.1"}, Params: sip.NewParams()}
		from.Params.Add("tag", generateID(8))
		target := sip.Uri{Scheme: "sip", User: contactUserFor("inbox16", ""), Host: "127.0.0.1", Port: s.cfg.SIPAdvertisePort}
		d, err := pbx.dialog.Invite(ctx, target, []byte(buildSDP("127.0.0.1", pbx.rtpPort(), ptPCMU)),
			from, sip.NewHeader("Content-Type", "application/sdp"))
		if err != nil {
			t.Fatal(err)
		}
		pbx.mu.Lock()
		pbx.cliDialog = d
		pbx.mu.Unlock()
		c := s.calls.get(awaitEvent(t, sub, "incoming")["id"].(string))
		go func() { _ = c.accept("ia") }()
		if err := d.WaitAnswer(ctx, sipgo.AnswerOptions{}); err != nil {
			t.Fatal(err)
		}
		_ = d.Ack(ctx)
		time.Sleep(50 * time.Millisecond)

		if err := s.sip.transfer(c, "202"); err != nil {
			t.Fatal(err)
		}
		if got := <-referred; got != "<sip:202@127.0.0.1>" {
			t.Fatalf("Refer-To %s", got)
		}
		// a IA sai da linha (consumidor some) durante a transferência: NÃO pode derrubar
		c.consumerGone("ws-closed")
		end := awaitEvent(t, sub, "call-ended")
		if end["reason"] != "transferred" {
			t.Fatalf("fim: %+v", end)
		}
		pbx.mu.Lock()
		pbx.cliDialog = nil
		pbx.mu.Unlock()
	})

	t.Run("modo register_in: PABX registra com digest e liga pelo tronco", func(t *testing.T) {
		tronco := Line{ID: "tronco1", AccountID: 1, Enabled: true, Mode: modeRegisterIn, User: "tronco", Password: "s3nha"}
		if err := tronco.normalize(nil); err != nil {
			t.Fatal(err)
		}
		_ = s.store.put(tronco)
		s.sip.applyLine(tronco)

		reg := func(pass string) int {
			aor := sip.Uri{Scheme: "sip", User: "tronco", Host: "127.0.0.1"}
			req := sip.NewRequest(sip.REGISTER, sip.Uri{Scheme: "sip", Host: "127.0.0.1", Port: s.cfg.SIPAdvertisePort})
			fh := &sip.FromHeader{Address: aor, Params: sip.NewParams()}
			fh.Params.Add("tag", generateID(8))
			req.AppendHeader(fh)
			req.AppendHeader(&sip.ToHeader{Address: aor})
			req.AppendHeader(&sip.ContactHeader{Address: sip.Uri{Scheme: "sip", User: "tronco", Host: "127.0.0.1", Port: pbx.port}})
			req.AppendHeader(sip.NewHeader("Expires", "300"))
			rctx, done := context.WithTimeout(ctx, 5*time.Second)
			defer done()
			res, err := pbx.dialog.Client.Do(rctx, req)
			if err != nil {
				t.Fatal(err)
			}
			if res.StatusCode == 401 {
				if res, err = pbx.dialog.Client.DoDigestAuth(rctx, req, res, sipgo.DigestAuth{Username: "tronco", Password: pass}); err != nil {
					t.Fatal(err)
				}
			}
			return res.StatusCode
		}
		if code := reg("errada"); code == 200 {
			t.Fatal("senha errada registrou")
		}
		if code := reg("s3nha"); code != 200 {
			t.Fatalf("registro com a senha certa: %d", code)
		}
		if st, _ := s.sip.lineStatus(tronco); st.State != regRegistered {
			t.Fatalf("status %+v", st)
		}

		// saída pelo tronco: vai para o PABX que registrou (o fake atende)
		c, err := s.sip.dial(tronco, nil, "1133334444", "tester")
		if err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(5 * time.Second)
		for c.currentStatus() != statusConnected && time.Now().Before(deadline) {
			time.Sleep(50 * time.Millisecond)
		}
		if c.currentStatus() != statusConnected {
			t.Fatal("saída pelo tronco não conectou")
		}
		c.hangup("user_ended")
		awaitEvent(t, sub, "call-ended")
	})

	t.Run("INVITE de origem desconhecida é recusado", func(t *testing.T) {
		if s.sip.trusts(line, "203.0.113.9") {
			t.Fatal("IP estranho aceito")
		}
	})
}

// 3CX: o ramal é "800"/"201", mas o digest usa o ID de autenticação. Registro e
// discagem têm que autenticar com o ID e continuar se identificando pelo ramal.
func TestAuthIDSeparadoDoRamal(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pbx := startFakePBX(t, ctx)
	pbx.creds = map[string][2]string{"800": {"Xk7pQ2mZ", "s3nha"}, "201": {"Ab12Cd34", "outra"}}
	s := startConnector(t, ctx)

	line := Line{ID: "inbox3cx", AccountID: 1, Enabled: true, Mode: modeRegisterOut, Host: "127.0.0.1",
		Port: pbx.port, User: "800", AuthUser: "Xk7pQ2mZ", Password: "s3nha",
		Agents: []Agent{{Email: "ana@botfy", Extension: "201", AuthUser: "Ab12Cd34", Password: "outra"}}}
	if err := line.normalize(nil); err != nil {
		t.Fatal(err)
	}
	_ = s.store.put(line)
	s.sip.applyLine(line)

	for i := 0; i < 2; i++ {
		select {
		case <-pbx.registered:
		case <-time.After(5 * time.Second):
			st, ag := s.sip.lineStatus(line)
			t.Fatalf("não registrou com o ID de autenticação: %+v %+v", st, ag)
		}
	}

	c, err := s.sip.dial(line, line.agentByEmail("ana@botfy"), "5511999990000", "tester")
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for c.currentStatus() != statusConnected && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if c.currentStatus() != statusConnected {
		t.Fatal("a discagem pelo ramal 201 não autenticou com o ID")
	}
	c.hangup("user_ended")

	pbx.mu.Lock()
	defer pbx.mu.Unlock()
	usados := map[string]bool{}
	for _, u := range pbx.authUsers {
		usados[u] = true
	}
	if !usados["Xk7pQ2mZ"] || !usados["Ab12Cd34"] || usados["800"] || usados["201"] {
		t.Fatalf("logins usados no digest: %v", pbx.authUsers)
	}
	if line.public()["authUser"] != "Xk7pQ2mZ" {
		t.Fatal("authUser não aparece na config pública")
	}
}

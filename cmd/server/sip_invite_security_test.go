package main

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
)

// INVITE com o From = usuário SIP de uma sessão: do IP do REGISTER passa como
// sempre; de outro IP só com a senha SIP (Digest).
func TestSIPInviteFromOtherIPNeedsDigest(t *testing.T) {
	log := slog.Default()
	mgr := &SessionManager{broker: NewBroker(), log: log, maxCalls: 8, sessions: map[string]*Session{}}
	sess := &Session{id: "s1", mgr: mgr, log: log, reg: newCallRegistry(), SIPUser: "wa_sec12345", SIPPass: "segredo"}
	sess.setSIPExt(sipExtConfig{Agent: sipAgentOpts{InboundMode: "agent"}})
	mgr.register(sess)
	gw, err := NewSIPGateway(mgr, log)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = gw.server.ListenAndServe(ctx, "udp", "127.0.0.1:15095") }()

	ua, _ := sipgo.NewUA()
	cli, _ := sipgo.NewClient(ua, sipgo.WithClientHostname("127.0.0.1"), sipgo.WithClientPort(15096))
	pbx, _ := sipgo.NewServer(ua)
	go func() { _ = pbx.ListenAndServe(ctx, "udp", "127.0.0.1:15096") }()
	time.Sleep(200 * time.Millisecond)

	register := func(ip string) {
		gw.mu.Lock()
		gw.registrations["wa_sec12345"] = &sipRegistration{
			sourceIP: ip, sipUser: "wa_sec12345", sessionID: "s1",
			contactURI: sip.Uri{Scheme: "sip", Host: ip, Port: 15096},
			expiresAt:  time.Now().Add(time.Minute),
		}
		gw.mu.Unlock()
	}
	first := func(auth string) int {
		target := sip.Uri{Scheme: "sip", User: "5561999990000", Host: "127.0.0.1", Port: 15095}
		req := sip.NewRequest(sip.INVITE, target)
		from := &sip.FromHeader{Address: sip.Uri{Scheme: "sip", User: "wa_sec12345", Host: "127.0.0.1"}, Params: sip.NewParams()}
		from.Params.Add("tag", "x"+auth[:min(len(auth), 3)])
		req.AppendHeader(from)
		req.AppendHeader(&sip.ToHeader{Address: target, Params: sip.NewParams()})
		req.AppendHeader(&sip.ContactHeader{Address: sip.Uri{Scheme: "sip", Host: "127.0.0.1", Port: 15096}})
		req.AppendHeader(sip.NewHeader("Content-Type", "application/sdp"))
		req.SetBody([]byte("v=0\r\no=- 1 1 IN IP4 127.0.0.1\r\ns=-\r\nc=IN IP4 127.0.0.1\r\nt=0 0\r\nm=audio 40000 RTP/AVP 0\r\n"))
		if auth != "" {
			req.AppendHeader(sip.NewHeader("Authorization", auth))
		}
		tctx, tc := context.WithTimeout(ctx, 3*time.Second)
		defer tc()
		tx, err := cli.TransactionRequest(tctx, req)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Terminate()
		for {
			select {
			case res := <-tx.Responses():
				if res.StatusCode == 100 {
					continue
				}
				return res.StatusCode
			case <-tctx.Done():
				t.Fatal("sem resposta ao INVITE")
			}
		}
	}

	register("10.9.9.9") // registro veio de OUTRO IP
	if code := first(""); code != 401 {
		t.Fatalf("INVITE de IP diferente sem senha: esperava 401, veio %d", code)
	}
	bad := buildAuth("wa_sec12345", "astracalls", "errada", "INVITE", "sip:5561999990000@127.0.0.1:15095", "n1")
	if code := first(bad); code != 401 {
		t.Fatalf("INVITE com senha errada: esperava 401, veio %d", code)
	}
	good := buildAuth("wa_sec12345", "astracalls", "segredo", "INVITE", "sip:5561999990000@127.0.0.1:15095", "n2")
	if code := first(good); code == 401 || code == 403 {
		t.Fatalf("INVITE com senha certa recusado: %d", code)
	}

	register("127.0.0.1") // mesmo IP do REGISTER: comportamento de sempre
	if code := first(""); code == 401 || code == 403 {
		t.Fatalf("INVITE do IP do registro recusado: %d", code)
	}
}

func TestByeMatchesDialog(t *testing.T) {
	mk := func(fromTag, toTag string) *sip.Request {
		r := sip.NewRequest(sip.BYE, sip.Uri{Scheme: "sip", Host: "x"})
		f := &sip.FromHeader{Address: sip.Uri{Host: "a"}, Params: sip.NewParams()}
		f.Params.Add("tag", fromTag)
		to := &sip.ToHeader{Address: sip.Uri{Host: "b"}, Params: sip.NewParams()}
		to.Params.Add("tag", toTag)
		r.AppendHeader(f)
		r.AppendHeader(to)
		return r
	}
	if !byeMatchesDialog(mk("pbx", "nos"), "pbx", "nos") || !byeMatchesDialog(mk("nos", "pbx"), "pbx", "nos") {
		t.Fatal("BYE do próprio diálogo recusado")
	}
	if byeMatchesDialog(mk("intruso", "nos"), "pbx", "nos") {
		t.Fatal("BYE de outro diálogo aceito")
	}
	if !byeMatchesDialog(mk("qualquer", "x"), "", "nos") {
		t.Fatal("sem tag conhecida deve aceitar (compatibilidade)")
	}
}

// Várias sessões registradas no mesmo PBX: o INVITE vai para a do ramal chamado.
func TestSessionForExtInviteByExtension(t *testing.T) {
	log := slog.Default()
	mgr := &SessionManager{broker: NewBroker(), log: log, maxCalls: 8, sessions: map[string]*Session{}}
	gw := &SIPGateway{sessions: mgr, log: log, extRegs: map[string]*sipUACRegistrar{}}
	add := func(id, user string) {
		s := &Session{id: id, mgr: mgr, log: log, reg: newCallRegistry()}
		s.setSIPExt(sipExtConfig{Enabled: true, Host: "200.1.1.1", User: user})
		mgr.register(s)
		gw.extRegs[id] = &sipUACRegistrar{gw: gw, sessionID: id, cfg: s.sipExtSnapshot()}
	}
	inv := func(user string) *sip.Request {
		r := sip.NewRequest(sip.INVITE, sip.Uri{Scheme: "sip", User: user, Host: "nos"})
		r.AppendHeader(&sip.ToHeader{Address: sip.Uri{Scheme: "sip", User: user, Host: "nos"}, Params: sip.NewParams()})
		r.SetSource("200.1.1.1:5060")
		return r
	}
	add("s1", "2001")
	if s := gw.sessionForExtInvite(inv("qualquer")); s == nil || s.id != "s1" {
		t.Fatal("uma sessão só no PBX: deve casar como antes")
	}
	add("s2", "2002")
	for i := 0; i < 20; i++ { // ordem do map é aleatória
		if s := gw.sessionForExtInvite(inv("2002")); s == nil || s.id != "s2" {
			t.Fatalf("ramal 2002 deveria ir para s2, foi %v", s)
		}
	}
	if s := gw.sessionForExtInvite(inv("9999")); s != nil {
		t.Fatalf("ramal desconhecido com 2 sessões: não deve escolher ao acaso (%s)", s.id)
	}
	other := inv("2001")
	other.SetSource("8.8.8.8:5060")
	if s := gw.sessionForExtInvite(other); s != nil {
		t.Fatal("IP de fora do PBX não pode casar")
	}
}

package main

import (
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
)

// O 200 do REGISTER precisa listar o binding no Contact (RFC 3261 §10.3): o
// 3CX confere e, sem ele, deixa o tronco "Não registrado" (sem rota de saída).
func TestSIPRegisterOKHasContact(t *testing.T) {
	log := slog.Default()
	mgr := &SessionManager{broker: NewBroker(), log: log, maxCalls: 8, sessions: map[string]*Session{}}
	mgr.register(&Session{id: "s1", mgr: mgr, log: log, reg: newCallRegistry(), SIPUser: "wa_test1234", SIPPass: "segredo"})

	gw, err := NewSIPGateway(mgr, log)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = gw.server.ListenAndServe(ctx, "udp", "127.0.0.1:15093") }()

	ua, _ := sipgo.NewUA(sipgo.WithUserAgent("3CX-test"))
	cli, _ := sipgo.NewClient(ua, sipgo.WithClientHostname("127.0.0.1"), sipgo.WithClientPort(15094))
	pbx, _ := sipgo.NewServer(ua) // recebe as respostas na porta do Via
	go func() { _ = pbx.ListenAndServe(ctx, "udp", "127.0.0.1:15094") }()
	time.Sleep(200 * time.Millisecond)

	target := sip.Uri{Scheme: "sip", Host: "127.0.0.1", Port: 15093}
	aor := sip.Uri{Scheme: "sip", User: "wa_test1234", Host: "127.0.0.1"}
	req := sip.NewRequest(sip.REGISTER, target)
	from := &sip.FromHeader{Address: aor, Params: sip.NewParams()}
	from.Params.Add("tag", "t1")
	req.AppendHeader(from)
	req.AppendHeader(&sip.ToHeader{Address: aor, Params: sip.NewParams()})
	req.AppendHeader(&sip.ContactHeader{Address: sip.Uri{Scheme: "sip", User: "wa_test1234", Host: "127.0.0.1", Port: 15094}})
	req.AppendHeader(sip.NewHeader("Expires", "120"))
	req.AppendHeader(sip.NewHeader("Authorization",
		buildAuth("wa_test1234", "astracalls", "segredo", "REGISTER", "sip:127.0.0.1:15093", "n1")))

	rctx, rcancel := context.WithTimeout(ctx, 3*time.Second)
	defer rcancel()
	res, err := cli.Do(rctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != 200 {
		t.Fatalf("esperava 200, got %d", res.StatusCode)
	}
	ct := res.Contact()
	if ct == nil || ct.Address.Port != 15094 || ct.Address.User != "wa_test1234" {
		t.Fatalf("200 sem o Contact registrado: %v", res)
	}
	if !strings.Contains(ct.String(), "expires=120") {
		t.Fatalf("Contact sem expires: %s", ct)
	}
}

// 3CX: o INVITE de saída vem com o caller ID do ramal no From (não o usuário
// SIP da sessão); a sessão é achada pelo IP do PBX registrado em nós.
func TestSessionForRegisteredPBX(t *testing.T) {
	log := slog.Default()
	mgr := &SessionManager{broker: NewBroker(), log: log, maxCalls: 8, sessions: map[string]*Session{}}
	mgr.register(&Session{id: "s1", mgr: mgr, log: log, reg: newCallRegistry()})
	mgr.register(&Session{id: "s2", mgr: mgr, log: log, reg: newCallRegistry()})

	gw, err := NewSIPGateway(mgr, log)
	if err != nil {
		t.Fatal(err)
	}
	reg := func(user, sessionID, host string, ttl time.Duration) {
		gw.registrations[user] = &sipRegistration{
			sipUser: user, sessionID: sessionID,
			contactURI: sip.Uri{Scheme: "sip", User: user, Host: host, Port: 5060},
			expiresAt:  time.Now().Add(ttl),
		}
	}
	reg("wa_aaaa1111", "s1", "159.65.164.237", time.Minute)
	reg("wa_bbbb2222", "s2", "10.0.0.5", time.Minute)
	reg("wa_cccc3333", "s2", "10.0.0.9", -time.Minute) // vencido

	if s := gw.sessionForRegisteredPBX("159.65.164.237"); s == nil || s.id != "s1" {
		t.Fatalf("IP do 3CX não casou com s1: %v", s)
	}
	if s := gw.sessionForRegisteredPBX("10.0.0.9"); s != nil {
		t.Fatalf("registro vencido casou com %s", s.id)
	}
	if s := gw.sessionForRegisteredPBX("203.0.113.7"); s != nil {
		t.Fatalf("IP desconhecido casou com %s", s.id)
	}
	// duas sessões atrás do mesmo PBX: ambíguo, não casa nenhuma.
	reg("wa_dddd4444", "s2", "159.65.164.237", time.Minute)
	if s := gw.sessionForRegisteredPBX("159.65.164.237"); s != nil {
		t.Fatalf("IP ambíguo casou com %s", s.id)
	}
}

// Chamada WhatsApp atendida pelo painel/ponte enquanto toca no PBX: o toque SIP é
// cancelado e marcado como "atendida por outro" (não derruba a chamada).
func TestCancelInboundRing(t *testing.T) {
	log := slog.Default()
	mgr := &SessionManager{broker: NewBroker(), log: log, maxCalls: 8, sessions: map[string]*Session{}}
	gw, err := NewSIPGateway(mgr, log)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	ring := &inboundRing{cancel: cancel}
	gw.inboundRinging["wa1"] = ring

	if !gw.cancelInboundRing("wa1") {
		t.Fatal("deveria haver toque para cancelar")
	}
	if ctx.Err() == nil || !ring.claimed {
		t.Fatalf("toque não cancelado/marcado: err=%v claimed=%v", ctx.Err(), ring.claimed)
	}
	if _, ok := gw.inboundRinging["wa1"]; ok {
		t.Fatal("toque continua registrado")
	}
	if gw.cancelInboundRing("wa1") || gw.cancelInboundRing("outra") {
		t.Fatal("sem toque registrado não deveria cancelar nada")
	}
}

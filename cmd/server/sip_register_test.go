package main

import (
	"log/slog"
	"testing"
	"time"

	"github.com/emiago/sipgo/sip"
)

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

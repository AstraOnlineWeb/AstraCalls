package main

import "testing"

func TestSessionOptionsEffective(t *testing.T) {
	s := &Session{}
	if o := s.effectiveOptions(); o.ReadMessages || o.AlwaysOnline {
		t.Fatalf("padrão deve vir tudo desligado, veio %+v", o)
	}
	// flag legado do Chatwoot continua valendo
	s.setChatwoot(ChatwootConfig{ReadMessages: true})
	if !s.readMessagesEnabled() || s.alwaysOnlineEnabled() {
		t.Fatalf("read_messages do Chatwoot deveria valer: %+v", s.effectiveOptions())
	}
	// opção da sessão sozinha
	s.setChatwoot(ChatwootConfig{})
	s.setOptions(sessionOptions{AlwaysOnline: true})
	if s.readMessagesEnabled() || !s.alwaysOnlineEnabled() {
		t.Fatalf("always_online da sessão deveria valer: %+v", s.effectiveOptions())
	}
}

package main

import (
	"encoding/json"
	"testing"
)

// O espelhamento no Chatwoot decide pela ORIGEM da mensagem: o que sai do agente
// do Chatwoot nunca é refletido (já está na conversa), o que sai da API é refletido
// quando mirror_api está ligado, e o que sai do aparelho não está registrado aqui.
func TestSelfSentOrigin(t *testing.T) {
	s := &Session{}
	s.markSelfSent("api-1", selfSentAPI)
	s.markSelfSent("cw-1", selfSentChatwoot)

	cases := []struct {
		name       string
		id         string
		wantOrigin string
		wantOK     bool
	}{
		{"enviada pela API", "api-1", selfSentAPI, true},
		{"enviada pelo agente do Chatwoot", "cw-1", selfSentChatwoot, true},
		{"enviada pelo aparelho (não registrada)", "phone-1", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			origin, ok := s.selfSentOrigin(c.id)
			if ok != c.wantOK || origin != c.wantOrigin {
				t.Fatalf("selfSentOrigin(%q) = (%q, %v), quer (%q, %v)", c.id, origin, ok, c.wantOrigin, c.wantOK)
			}
		})
	}
}

// ID vazio não deve ser registrado (SendMessage pode devolver ID vazio em falha).
func TestMarkSelfSentIgnoraIDVazio(t *testing.T) {
	s := &Session{}
	s.markSelfSent("", selfSentAPI)
	if _, ok := s.selfSentOrigin(""); ok {
		t.Fatal("ID vazio não deveria ser registrado")
	}
}

// Regra de espelhamento (shouldMirrorOwn): só a API com mirror_api ligado é
// refletida; o agente do Chatwoot nunca; o aparelho sempre.
func TestShouldMirrorOwn(t *testing.T) {
	s := &Session{}
	s.markSelfSent("api-1", selfSentAPI)
	s.markSelfSent("cw-1", selfSentChatwoot)

	cases := []struct {
		name      string
		msgID     string
		mirrorAPI bool
		want      bool
	}{
		{"aparelho espelha mesmo com toggle off", "phone-1", false, true},
		{"aparelho espelha com toggle on", "phone-1", true, true},
		{"API com toggle ligado espelha", "api-1", true, true},
		{"API com toggle desligado não espelha", "api-1", false, false},
		{"agente do Chatwoot nunca espelha", "cw-1", true, false},
		{"agente do Chatwoot nunca espelha (toggle off)", "cw-1", false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg := ChatwootConfig{MirrorAPI: c.mirrorAPI}
			if got := s.shouldMirrorOwn(c.msgID, cfg); got != c.want {
				t.Fatalf("shouldMirrorOwn(%q, mirror_api=%v) = %v, quer %v", c.msgID, c.mirrorAPI, got, c.want)
			}
		})
	}
}

// A config vinda do painel não pode zerar flags que ele não envia (regressão:
// salvar no painel apagava groups/sign_msg porque o decode partia do zero).
func TestSetChatwootPreservaFlagsAusentes(t *testing.T) {
	atual := ChatwootConfig{
		URL: "https://cw.exemplo", AccountID: 1, AccountToken: "tok", InboxID: 5,
		Groups: true, SignMsg: true, MirrorAPI: true,
	}
	payload := `{"url":"https://cw.exemplo","account_id":1,"inbox_id":5,"inbox_identifier":"abc"}`

	cfg := atual // mesma semântica do handler: decodifica POR CIMA da config atual
	if err := json.Unmarshal([]byte(payload), &cfg); err != nil {
		t.Fatal(err)
	}
	if !cfg.Groups || !cfg.SignMsg || !cfg.MirrorAPI {
		t.Fatalf("flags ausentes no payload foram zeradas: %+v", cfg)
	}
	if cfg.InboxIdentifier != "abc" {
		t.Fatalf("campo presente no payload não foi aplicado: %+v", cfg)
	}
}

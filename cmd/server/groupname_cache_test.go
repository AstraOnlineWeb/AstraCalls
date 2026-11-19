package main

import (
	"errors"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/types"
)

// Nome de grupo nos fluxos automáticos: 1 consulta ao WhatsApp por grupo a cada
// avatarTTL, e falha (ex.: rate-overlimit) não vira 1 tentativa por mensagem.
func TestGroupNameCache(t *testing.T) {
	var c avatarCache
	now := time.Now()
	const key = "120363000000000000@g.us"

	calls := 0
	ok := func() (string, error) { calls++; return "Grupo A", nil }
	for i := 0; i < 100; i++ {
		if n := c.get(key, now, ok); n != "Grupo A" {
			t.Fatalf("nome = %q", n)
		}
	}
	if calls != 1 {
		t.Fatalf("100 mensagens geraram %d consultas, esperado 1", calls)
	}

	// renomeado (events.GroupInfo): passa a valer o nome novo sem consultar
	c.set(key, "Grupo B", now)
	if n := c.get(key, now, ok); n != "Grupo B" || calls != 1 {
		t.Fatalf("após renomear: nome=%q consultas=%d", n, calls)
	}
	// depois do TTL consulta de novo
	if c.get(key, now.Add(avatarTTL+time.Second), ok); calls != 2 {
		t.Fatalf("após o TTL esperava nova consulta, consultas=%d", calls)
	}

	fails := 0
	bad := func() (string, error) { fails++; return "", errors.New("rate-overlimit") }
	const key2 = "120363000000000001@g.us"
	for i := 0; i < 50; i++ {
		if n := c.get(key2, now, bad); n != "" {
			t.Fatalf("falha deveria devolver vazio, veio %q", n)
		}
	}
	if fails != 1 {
		t.Fatalf("50 mensagens com falha geraram %d tentativas, esperado 1", fails)
	}
	if c.get(key2, now.Add(avatarErrTTL+time.Second), bad); fails != 2 {
		t.Fatalf("após o TTL de erro esperava nova tentativa, tentativas=%d", fails)
	}
}

func TestAllowGroupRefresh(t *testing.T) {
	s := &Session{}
	g := types.NewJID("120363000000000000", types.GroupServer)
	now := time.Now()
	if !s.allowGroupRefresh(g, now) {
		t.Fatal("primeira rebusca deveria passar")
	}
	if s.allowGroupRefresh(g, now.Add(time.Minute)) {
		t.Fatal("rebusca dentro do intervalo deveria ser barrada")
	}
	if !s.allowGroupRefresh(g, now.Add(groupRefreshInterval+time.Second)) {
		t.Fatal("rebusca depois do intervalo deveria passar")
	}
}

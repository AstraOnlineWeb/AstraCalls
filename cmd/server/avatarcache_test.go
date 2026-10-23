package main

import (
	"errors"
	"testing"
	"time"

	"go.mau.fi/whatsmeow"
)

func TestAvatarCache(t *testing.T) {
	var c avatarCache
	now := time.Now()
	calls := 0
	fetch := func(url string, err error) func() (string, error) {
		return func() (string, error) { calls++; return url, err }
	}

	// foto encontrada: 1 busca, depois só cache até o TTL
	if u := c.get("a", now, fetch("https://x/a.jpg", nil)); u != "https://x/a.jpg" {
		t.Fatalf("url = %q", u)
	}
	c.get("a", now.Add(time.Hour), fetch("outra", nil))
	if calls != 1 {
		t.Fatalf("buscou %d vezes, esperado 1", calls)
	}
	if u := c.get("a", now.Add(avatarTTL+time.Second), fetch("https://x/a2.jpg", nil)); u != "https://x/a2.jpg" || calls != 2 {
		t.Fatalf("após o TTL: url=%q calls=%d", u, calls)
	}

	// foto escondida (401): a ausência também fica em cache
	calls = 0
	c.get("b", now, fetch("", whatsmeow.ErrProfilePictureUnauthorized))
	c.get("b", now.Add(time.Hour), fetch("", whatsmeow.ErrProfilePictureUnauthorized))
	if calls != 1 {
		t.Fatalf("401 buscou %d vezes, esperado 1", calls)
	}

	// falha passageira: tenta de novo depois de avatarErrTTL
	calls = 0
	c.get("c", now, fetch("", errors.New("timeout")))
	c.get("c", now.Add(time.Minute), fetch("", errors.New("timeout")))
	if calls != 1 {
		t.Fatalf("erro buscou %d vezes antes do TTL curto", calls)
	}
	if u := c.get("c", now.Add(avatarErrTTL+time.Second), fetch("https://x/c.jpg", nil)); u != "https://x/c.jpg" || calls != 2 {
		t.Fatalf("após TTL de erro: url=%q calls=%d", u, calls)
	}
}

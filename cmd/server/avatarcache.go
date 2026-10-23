package main

import (
	"context"
	"errors"
	"sync"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"
)

// Foto de perfil para o Chatwoot: buscar a cada mensagem gerava 1 IQ
// w:profile:picture por mensagem recebida — e quem esconde a foto respondia 401
// toda vez. O cache guarda a URL e também a ausência (sem foto/sem permissão).
const (
	avatarTTL      = 6 * time.Hour
	avatarErrTTL   = 5 * time.Minute // falha passageira (timeout, desconectado)
	avatarCacheMax = 5000
)

type avatarEntry struct {
	url string
	exp time.Time
}

type avatarCache struct {
	mu sync.Mutex
	m  map[string]avatarEntry
}

// get devolve a URL em cache ou chama fetch e guarda o resultado. fetch roda
// fora do lock; duas buscas simultâneas do mesmo contato no máximo se repetem.
func (c *avatarCache) get(key string, now time.Time, fetch func() (string, error)) string {
	c.mu.Lock()
	if e, ok := c.m[key]; ok && now.Before(e.exp) {
		c.mu.Unlock()
		return e.url
	}
	c.mu.Unlock()

	url, err := fetch()
	ttl := avatarTTL
	if err != nil {
		url = ""
		if !errors.Is(err, whatsmeow.ErrProfilePictureUnauthorized) && !errors.Is(err, whatsmeow.ErrProfilePictureNotSet) {
			ttl = avatarErrTTL
		}
	}

	c.mu.Lock()
	if c.m == nil {
		c.m = make(map[string]avatarEntry)
	}
	if len(c.m) >= avatarCacheMax {
		for k, e := range c.m {
			if !now.Before(e.exp) {
				delete(c.m, k)
			}
		}
		if len(c.m) >= avatarCacheMax {
			c.m = make(map[string]avatarEntry)
		}
	}
	c.m[key] = avatarEntry{url: url, exp: now.Add(ttl)}
	c.mu.Unlock()
	return url
}

// cachedAvatar é a foto de perfil (contato ou grupo) usada nos fluxos
// automáticos; "" quando não há foto visível.
func (s *Session) cachedAvatar(jid types.JID) string {
	if jid.IsEmpty() {
		return ""
	}
	return s.avatars.get(jid.ToNonAD().String(), time.Now(), func() (string, error) {
		pp, err := s.client.GetProfilePictureInfo(context.Background(), jid, nil)
		if err != nil || pp == nil {
			return "", err
		}
		return pp.URL, nil
	})
}

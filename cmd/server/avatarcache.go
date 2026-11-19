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

// set grava um valor já conhecido (ex.: nome novo vindo do evento de grupo),
// sem consultar o WhatsApp.
func (c *avatarCache) set(key, val string, now time.Time) {
	c.mu.Lock()
	if c.m == nil {
		c.m = make(map[string]avatarEntry)
	}
	c.m[key] = avatarEntry{url: val, exp: now.Add(avatarTTL)}
	c.mu.Unlock()
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

// cachedGroupName é o nome do grupo usado nos fluxos automáticos (cada mensagem
// de grupo no Chatwoot, voto de enquete, visualização única). GetGroupInfo é um
// IQ ao servidor sem cache no whatsmeow: chamar a cada mensagem gerava milhares
// de consultas por dia e levava o número à restrição. Mesmo cache/TTL da foto;
// falha (ex.: rate-overlimit) fica 5 min sem nova tentativa. "" quando não há
// nome — o chamador usa o JID.
func (s *Session) cachedGroupName(group types.JID) string {
	if group.IsEmpty() {
		return ""
	}
	return s.groupNames.get(group.ToNonAD().String(), time.Now(), func() (string, error) {
		gi, err := s.client.GetGroupInfo(context.Background(), group)
		if err != nil || gi == nil {
			return "", err
		}
		return gi.Name, nil
	})
}

// updateGroupName grava o nome novo quando o grupo é renomeado (events.GroupInfo).
func (s *Session) updateGroupName(group types.JID, name string) {
	if group.IsEmpty() || name == "" {
		return
	}
	s.groupNames.set(group.ToNonAD().String(), name, time.Now())
}

// cachedChannelName: idem para canais (newsletter) — GetNewsletterInfo também é
// um IQ por chamada e rodava a cada mensagem de canal.
func (s *Session) cachedChannelName(channel types.JID) string {
	if channel.IsEmpty() {
		return ""
	}
	return s.groupNames.get(channel.String(), time.Now(), func() (string, error) {
		ni, err := s.client.GetNewsletterInfo(context.Background(), channel)
		if err != nil || ni == nil {
			return "", err
		}
		return ni.ThreadMeta.Name.Text, nil
	})
}

// groupRefreshInterval limita o reenvio após ErrNoPermissionInGroup a uma
// consulta GetGroupInfo por grupo nesse intervalo (senão cada envio recusado
// num grupo só de admins viraria uma consulta ao WhatsApp).
const groupRefreshInterval = avatarErrTTL

// allowGroupRefresh devolve true se o grupo não foi rebuscado no intervalo.
func (s *Session) allowGroupRefresh(group types.JID, now time.Time) bool {
	key := group.ToNonAD().String()
	if v, ok := s.groupRefreshed.Load(key); ok && now.Sub(v.(time.Time)) < groupRefreshInterval {
		return false
	}
	s.groupRefreshed.Store(key, now)
	return true
}

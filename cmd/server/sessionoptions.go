package main

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"go.mau.fi/whatsmeow/types"
)

// Opções de comportamento da conta no WhatsApp, por sessão (independem do
// Chatwoot). Os flags homônimos da config do Chatwoot (read_messages,
// always_online) continuam valendo por compatibilidade: basta um dos dois.
type sessionOptions struct {
	// ReadMessages: confirma leitura (tique azul) de toda mensagem recebida.
	ReadMessages bool `json:"read_messages"`
	// AlwaysOnline: mantém a conta "online". Com um dispositivo vinculado online
	// o WhatsApp entende que a pessoa está no computador e NÃO notifica o
	// celular; por isso o padrão é ficar online só por um instante.
	AlwaysOnline bool `json:"always_online"`
}

// presenceOnlinePulse é quanto tempo a conta fica "online" a cada reforço de
// presença quando AlwaysOnline está desligado. Basta para o WhatsApp registrar
// atividade do dispositivo (evita "Será desconectado hoje") e depois voltamos a
// "offline" para o celular continuar recebendo notificações.
const presenceOnlinePulse = time.Minute

func (s *Session) setOptions(o sessionOptions) {
	s.mu.Lock()
	s.options = o
	s.mu.Unlock()
}

func (s *Session) getOptions() sessionOptions {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.options
}

func (s *Session) readMessagesEnabled() bool {
	return s.getOptions().ReadMessages || s.getChatwoot().ReadMessages
}

func (s *Session) alwaysOnlineEnabled() bool {
	return s.getOptions().AlwaysOnline || s.getChatwoot().AlwaysOnline
}

// scheduleOffline volta a conta para "offline" depois do pulso de presença,
// a menos que AlwaysOnline esteja ligado. Um novo pulso reinicia a contagem.
func (s *Session) scheduleOffline() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.offlineTimer != nil {
		s.offlineTimer.Stop()
	}
	s.offlineTimer = time.AfterFunc(presenceOnlinePulse, func() {
		s.sendOfflineIfAllowed(s.mgr.appCtx)
	})
}

func (s *Session) sendOfflineIfAllowed(ctx context.Context) {
	if s.alwaysOnlineEnabled() || !s.client.IsConnected() || !s.client.IsLoggedIn() {
		return
	}
	if err := s.client.SendPresence(ctx, types.PresenceUnavailable); err != nil {
		s.log.Debug("presença offline falhou", "err", err)
		return
	}
	s.log.Info("presença offline enviada (celular volta a notificar)")
}

// GET /api/sessions/{sid}/options → sessionOptions
func (s *server) handleGetOptions(w http.ResponseWriter, r *http.Request) {
	sess := s.sessionByID(w, r.PathValue("sid"))
	if sess == nil {
		return
	}
	writeJSON(w, http.StatusOK, sess.effectiveOptions())
}

// effectiveOptions junta as opções da sessão com os flags legados do Chatwoot.
func (sess *Session) effectiveOptions() sessionOptions {
	return sessionOptions{ReadMessages: sess.readMessagesEnabled(), AlwaysOnline: sess.alwaysOnlineEnabled()}
}

// PUT /api/sessions/{sid}/options { "read_messages": bool, "always_online": bool }
// Campos ausentes mantêm o valor atual.
func (s *server) handleSetOptions(w http.ResponseWriter, r *http.Request) {
	sess := s.sessionByID(w, r.PathValue("sid"))
	if sess == nil {
		return
	}
	o := sess.effectiveOptions()
	if err := json.NewDecoder(r.Body).Decode(&o); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "payload inválido"})
		return
	}
	b, _ := json.Marshal(o)
	if err := sess.mgr.store.setOptions(r.Context(), sess.id, string(b)); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	sess.setOptions(o)
	// Os flags legados do Chatwoot somam com os da sessão: alinha para que
	// desligar aqui desligue de fato.
	if cw := sess.getChatwoot(); cw.ReadMessages != o.ReadMessages || cw.AlwaysOnline != o.AlwaysOnline {
		cw.ReadMessages, cw.AlwaysOnline = o.ReadMessages, o.AlwaysOnline
		sess.setChatwoot(cw)
		if cw.valid() {
			if cb, err := json.Marshal(cw); err == nil {
				_ = sess.mgr.store.setChatwoot(r.Context(), sess.id, string(cb))
			}
		}
	}
	// aplica a presença na hora
	if sess.client.IsConnected() && sess.client.IsLoggedIn() {
		if o.AlwaysOnline {
			go sess.keepPresenceActive(sess.mgr.appCtx)
		} else {
			go sess.sendOfflineIfAllowed(sess.mgr.appCtx)
		}
	}
	writeJSON(w, http.StatusOK, o)
}

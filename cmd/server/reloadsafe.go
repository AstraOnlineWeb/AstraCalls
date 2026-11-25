package main

import (
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"wacalls/internal/voip/core"
)

// Modo reload-safe da ligação do atendente (WhatsApp).
//
// Sem ele, quando a ponte do navegador cai (ICE failed/closed — o que acontece
// ao recarregar a página — ou o WebSocket fecha), a ligação com o WhatsApp é
// encerrada. Com ele (opt-in: "reloadSafe": true no /webrtc, ?reloadSafe=1 no
// /ws, ou WACALLS_RELOAD_SAFE=1 como padrão), a ligação NÃO cai: o cliente vai
// para a espera com música, sai o evento SSE call-webrtc-failed
// {reason, graceSeconds} e o servidor espera o atendente reconectar
// (/webrtc ou /ws de novo, de qualquer aba com o mesmo dono). Ao reconectar, a
// chamada sai da espera sozinha (exceto durante uma consulta). Se ninguém
// reconectar dentro do prazo (graceSeconds, padrão 60 s, máx. 600), aí sim
// encerra.

const (
	reloadSafeDefaultGrace = 60 * time.Second
	reloadSafeMaxGrace     = 10 * time.Minute
)

type reloadSafeOpts struct {
	enabled bool
	grace   time.Duration
}

func reloadSafeDefault() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("WACALLS_RELOAD_SAFE")))
	return v == "1" || v == "true" || v == "yes"
}

func reloadSafeFrom(flag *bool, graceSeconds int) reloadSafeOpts {
	o := reloadSafeOpts{enabled: reloadSafeDefault(), grace: reloadSafeDefaultGrace}
	if flag != nil {
		o.enabled = *flag
	}
	if graceSeconds > 0 {
		o.grace = time.Duration(graceSeconds) * time.Second
	}
	if o.grace > reloadSafeMaxGrace {
		o.grace = reloadSafeMaxGrace
	}
	return o
}

func reloadSafeFromQuery(r *http.Request) reloadSafeOpts {
	q := r.URL.Query()
	var flag *bool
	if v := strings.ToLower(q.Get("reloadSafe")); v != "" {
		b := v == "1" || v == "true" || v == "yes"
		flag = &b
	}
	g, _ := strconv.Atoi(q.Get("graceSeconds"))
	return reloadSafeFrom(flag, g)
}

// orphanCall: a ponte do atendente caiu em modo reload-safe. stillCurrent diz
// se a ponte que caiu ainda é a da chamada (se já foi trocada, nada a fazer).
func (s *server) orphanCall(sess *Session, ac *activeCall, callID string, stillCurrent func() bool, grace time.Duration, reason string) {
	if !stillCurrent() {
		return
	}
	if cr, ok := s.broker.getCall(callID); !ok || cr == nil || cr.Status == StatusEnded {
		return
	}
	_ = ac.cm.HoldWith(true, sess.mohFor("")) // já em espera = ok
	ac.orphanMu.Lock()
	if ac.orphanTimer != nil {
		ac.orphanTimer.Stop()
	}
	ac.orphanTimer = time.AfterFunc(grace, func() {
		ac.orphanMu.Lock()
		ac.orphanTimer = nil
		ac.orphanMu.Unlock()
		if stillCurrent() {
			s.log.Info("reload-safe: atendente não voltou no prazo; encerrando", "call", callID)
			sess.terminateCall(callID, core.EndCallReasonUserEnded)
		}
	})
	ac.orphanMu.Unlock()
	s.log.Info("reload-safe: ponte do atendente caiu; chamada em espera aguardando reconexão", "call", callID, "reason", reason, "grace", grace)
	s.broker.broadcastForSession(sess.id, map[string]any{
		"type": "call-webrtc-failed", "sessionId": sess.id, "id": callID, "reason": reason,
		"graceSeconds": int(grace / time.Second), "reloadSafe": true,
	})
}

// reattached: uma ponte nova (WebRTC ou WS) foi instalada na chamada. Se ela
// estava órfã (reload-safe), cancela o encerramento e tira da espera.
func (s *Session) reattached(callID string) {
	ac, ok := s.reg.get(callID)
	if !ok {
		return
	}
	ac.orphanMu.Lock()
	t := ac.orphanTimer
	ac.orphanTimer = nil
	ac.orphanMu.Unlock()
	if t == nil {
		return
	}
	t.Stop()
	if ac.consult.Load() == nil {
		_ = ac.cm.Resume()
	}
	s.log.Info("reload-safe: atendente reconectou; chamada retomada", "call", callID)
	s.mgr.broker.broadcastForSession(s.id, map[string]any{
		"type": "call-agent-reconnected", "sessionId": s.id, "id": callID,
	})
}

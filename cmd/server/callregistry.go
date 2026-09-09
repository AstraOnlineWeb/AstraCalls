package main

import (
	"sync"
	"sync/atomic"
	"time"

	"wacalls/internal/voip/call"
	"wacalls/internal/voip/media"
)

type activeCall struct {
	cm          *call.CallManager
	bridge      *Bridge
	wsBridge    *wsBridge // ponte WebSocket (alternativa ao pion WebRTC para proxies HTTP)
	browserOpus media.Codec
	recorder    *callRecorder // nil quando a gravação está desligada na sessão
	rtpBridge   *SIPRTPBridge // ponte RTP p/ SIP; nil quando a chamada não é SIP
	peerAudioN  uint64        // diagnóstico: nº de frames de áudio do peer (WhatsApp) recebidos
	ringTimer   *time.Timer   // timeout de toque: expira a chamada que nunca recebe encerramento
	answered    atomic.Bool   // marca que a chamada ficou ativa (atendida) — não expirar
}

type callRegistry struct {
	mu    sync.Mutex
	calls map[string]*activeCall
}

func newCallRegistry() *callRegistry {
	return &callRegistry{calls: map[string]*activeCall{}}
}

func (r *callRegistry) add(callID string, ac *activeCall) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls[callID] = ac
}

func (r *callRegistry) get(callID string) (*activeCall, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	ac, ok := r.calls[callID]
	return ac, ok
}

func (r *callRegistry) remove(callID string) (*activeCall, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	ac, ok := r.calls[callID]
	if !ok {
		return nil, false
	}
	if ac.ringTimer != nil {
		ac.ringTimer.Stop()
	}
	delete(r.calls, callID)
	return ac, true
}

// setRingTimer guarda o timer de timeout de toque na chamada. Se a chamada já
// saiu do registro (encerrou antes de armar), para o timer na hora.
func (r *callRegistry) setRingTimer(callID string, t *time.Timer) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if ac, ok := r.calls[callID]; ok {
		ac.ringTimer = t
		return
	}
	t.Stop()
}

// stopRingTimer cancela o timer de toque (chamada atendida) sob o lock.
func (r *callRegistry) stopRingTimer(callID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if ac, ok := r.calls[callID]; ok && ac.ringTimer != nil {
		ac.ringTimer.Stop()
		ac.ringTimer = nil
	}
}

func (r *callRegistry) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.calls)
}

func (r *callRegistry) setBridge(callID string, b *Bridge, oc media.Codec) (*Bridge, media.Codec, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	ac, ok := r.calls[callID]
	if !ok {
		return nil, nil, false
	}
	oldB, oldOC := ac.bridge, ac.browserOpus
	ac.bridge, ac.browserOpus = b, oc
	return oldB, oldOC, true
}

func (r *callRegistry) setWSBridge(callID string, b *wsBridge, oc media.Codec) (*wsBridge, *Bridge, media.Codec, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	ac, ok := r.calls[callID]
	if !ok {
		return nil, nil, nil, false
	}
	oldWSB, oldB, oldOC := ac.wsBridge, ac.bridge, ac.browserOpus
	ac.wsBridge, ac.bridge, ac.browserOpus = b, nil, oc
	return oldWSB, oldB, oldOC, true
}

func (r *callRegistry) drain() []*activeCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*activeCall, 0, len(r.calls))
	for _, ac := range r.calls {
		if ac.ringTimer != nil {
			ac.ringTimer.Stop()
		}
		out = append(out, ac)
	}
	r.calls = map[string]*activeCall{}
	return out
}

func (r *callRegistry) setRTPBridge(callID string, b *SIPRTPBridge) (*SIPRTPBridge, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	ac, ok := r.calls[callID]
	if !ok {
		return nil, false
	}
	oldB := ac.rtpBridge
	ac.rtpBridge = b
	return oldB, true
}

package main

import (
	"sync"

	"wacalls/internal/voip/call"
	"wacalls/internal/voip/media"
)

type activeCall struct {
	cm          *call.CallManager
	bridge      *Bridge
	wsBridge    *wsBridge // ponte WebSocket (alternativa ao pion WebRTC para proxies HTTP)
	browserOpus media.Codec
	recorder    *callRecorder // nil quando a gravação está desligada na sessão
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
	delete(r.calls, callID)
	return ac, true
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
		out = append(out, ac)
	}
	r.calls = map[string]*activeCall{}
	return out
}

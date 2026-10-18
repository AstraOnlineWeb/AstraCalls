package main

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
)

// Perna WebSocket "pcm16" — mesmo contrato do ws_bridge.go do wacalls, consumido
// pela ponte de voz do AstraChat (bridge.mjs):
//   - binário = PCM16 LE mono 16 kHz nos dois sentidos;
//   - texto (só com ?events=1) = eventos JSON de ciclo de vida (call-status, call-ended).
type wsLeg struct {
	conn   *websocket.Conn
	ctx    context.Context
	cancel context.CancelFunc
	events bool

	mu       sync.Mutex
	replaced atomic.Bool
	closed   atomic.Bool
}

func (l *wsLeg) writePCM(pcm []byte) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed.Load() {
		return
	}
	_ = l.conn.Write(l.ctx, websocket.MessageBinary, pcm)
}

func (l *wsLeg) sendEvent(ev map[string]any) {
	if !l.events {
		return
	}
	data, err := json.Marshal(ev)
	if err != nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed.Load() {
		return
	}
	_ = l.conn.Write(l.ctx, websocket.MessageText, data)
}

func (l *wsLeg) close() {
	if l.closed.Swap(true) {
		return
	}
	l.cancel()
	_ = l.conn.Close(websocket.StatusNormalClosure, "call closed")
}

// GET /api/sessions/{sid}/calls/{id}/ws
func (s *server) handleWS(w http.ResponseWriter, r *http.Request, p principal) {
	c := s.callFor(w, r, p)
	if c == nil {
		return
	}
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		Subprotocols:       []string{"pcm16"},
		InsecureSkipVerify: true, // a origem é validada pela credencial
	})
	if err != nil {
		s.log.Warn("ws: upgrade falhou", "call", c.ID, "err", err)
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	q := r.URL.Query().Get("events")
	leg := &wsLeg{conn: conn, ctx: ctx, cancel: cancel, events: q == "1" || q == "true"}
	c.attachWS(leg)
	s.log.Info("ws: conectado", "call", c.ID, "events", leg.events)

	go func() {
		t := time.NewTicker(20 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				pctx, pc := context.WithTimeout(ctx, 5*time.Second)
				_ = conn.Ping(pctx)
				pc()
			}
		}
	}()

	for {
		mt, data, err := conn.Read(ctx)
		if err != nil {
			break
		}
		if mt == websocket.MessageBinary && len(data) > 1 {
			c.feedPCM16(data)
		}
	}
	// Fechou sem ter sido trocado por outro consumidor: quem estava na linha saiu,
	// a chamada acaba (mesma semântica do OnTerminalWS do wacalls).
	if !leg.replaced.Load() && !leg.closed.Swap(true) {
		cancel()
		c.consumerGone("ws-closed")
	}
	s.log.Info("ws: desconectado", "call", c.ID)
}

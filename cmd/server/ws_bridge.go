package main

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"
	"wacalls/internal/voip/core"
)

// wsBridge é a ponte de áudio por WebSocket da chamada: transporte alternativo ao
// pion/WebRTC para consumidores que passam por proxy reverso HTTP (Cloudflare,
// Nginx, Traefik…) onde UDP é bloqueado, e a via natural para um serviço externo
// (ex.: agente de voz por IA) consumir o áudio da chamada dos dois lados.
//
// Formato de fio (contrato "pcm16"):
//   - Frames BINÁRIOS = áudio PCM Int16 LE, mono, 16 kHz, nos dois sentidos.
//     Downlink (WhatsApp→consumidor) e uplink (consumidor→WhatsApp) usam o MESMO
//     formato. O áudio trafega como PCM puro — sem Opus — para menos CPU e menos
//     latência (importante em volume).
//   - Frames de TEXTO = eventos de ciclo de vida em JSON, enviados só quando o
//     consumidor abre o socket com ?events=1 (ex.: {"type":"call-status",...},
//     {"type":"call-ended","reason":"..."}). O painel/widget abre sem ?events e
//     recebe só áudio.
type wsBridge struct {
	conn   *websocket.Conn
	ctx    context.Context
	cancel context.CancelFunc
	mu     sync.Mutex
	closed bool
	log    *slog.Logger
	events bool // envia eventos de ciclo de vida como frames de texto JSON

	// OnBrowserPCM recebe o uplink (consumidor→WhatsApp) já como PCM float32 16 kHz.
	OnBrowserPCM func(pcm16 []float32)
	// OnTerminalWS é disparado quando o socket fecha (encerra a chamada).
	OnTerminalWS func()
}

func newWSBridge(conn *websocket.Conn, log *slog.Logger, events bool) *wsBridge {
	ctx, cancel := context.WithCancel(context.Background())
	return &wsBridge{conn: conn, ctx: ctx, cancel: cancel, log: log, events: events}
}

// pcmFloatToInt16LE converte PCM float32 [-1,1] em bytes PCM Int16 little-endian.
func pcmFloatToInt16LE(pcm []float32) []byte {
	buf := make([]byte, len(pcm)*2)
	for i, v := range pcm {
		if v > 1 {
			v = 1
		} else if v < -1 {
			v = -1
		}
		var iv int16
		if v < 0 {
			iv = int16(v * 32768)
		} else {
			iv = int16(v * 32767)
		}
		binary.LittleEndian.PutUint16(buf[i*2:], uint16(iv))
	}
	return buf
}

// WritePCM envia o áudio do peer (downlink: WhatsApp→consumidor) como PCM16 LE
// mono 16 kHz, sem passar por Opus.
func (b *wsBridge) WritePCM(pcm16 []float32) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed || len(pcm16) == 0 {
		return nil
	}
	return b.conn.Write(b.ctx, websocket.MessageBinary, pcmFloatToInt16LE(pcm16))
}

// SendEvent envia um evento de ciclo de vida como frame de TEXTO JSON — apenas
// quando o consumidor abriu o socket com ?events=1. O áudio segue em binário.
func (b *wsBridge) SendEvent(ev map[string]any) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed || !b.events {
		return
	}
	data, err := json.Marshal(ev)
	if err != nil {
		return
	}
	_ = b.conn.Write(b.ctx, websocket.MessageText, data)
}

// DisableTerminate evita que o fechamento dispare OnTerminalWS (usado em
// renegociação/transferência, igual ao Bridge.DisableTerminate).
func (b *wsBridge) DisableTerminate() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.closed = true
}

// Close encerra o WebSocket.
func (b *wsBridge) Close() {
	b.mu.Lock()
	alreadyClosed := b.closed
	b.closed = true
	b.mu.Unlock()
	b.cancel()
	if !alreadyClosed {
		_ = b.conn.Close(websocket.StatusNormalClosure, "bridge closed")
	}
}

// readLoop lê frames PCM Int16 LE 16 kHz do consumidor (uplink) e entrega ao
// CallManager via OnBrowserPCM, sem Opus. Bloqueia até o WebSocket fechar.
func (b *wsBridge) readLoop() {
	for {
		mt, data, err := b.conn.Read(b.ctx)
		if err != nil {
			break
		}
		if mt != websocket.MessageBinary || len(data) == 0 || b.OnBrowserPCM == nil {
			continue
		}
		samples := make([]float32, len(data)/2)
		for i := range samples {
			iv := int16(binary.LittleEndian.Uint16(data[i*2:]))
			samples[i] = float32(iv) / 32768.0
		}
		b.OnBrowserPCM(samples)
	}

	b.mu.Lock()
	fireCallback := !b.closed
	b.closed = true
	b.mu.Unlock()

	if fireCallback && b.OnTerminalWS != nil {
		b.OnTerminalWS()
	}
}

// keepAlive envia pings a cada 20 s para manter a conexão viva através do
// Cloudflare (timeout de idle padrão: 100 s).
func (b *wsBridge) keepAlive() {
	t := time.NewTicker(20 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-b.ctx.Done():
			return
		case <-t.C:
			ctx, cancel := context.WithTimeout(b.ctx, 5*time.Second)
			err := b.conn.Ping(ctx)
			cancel()
			if err != nil {
				return
			}
		}
	}
}

// =============================================================================
// Handler HTTP
// =============================================================================

// handleWSBridge atende GET /api/sessions/{sid}/calls/{id}/ws
//
// Query:
//   - events=1  → além do áudio (binário), envia eventos de ciclo de vida da
//     chamada como frames de texto JSON (call-status, call-ended+reason).
func (s *server) handleWSBridge(w http.ResponseWriter, r *http.Request) {
	sid := r.PathValue("sid")
	callID := r.PathValue("id")

	sess := s.sessionByID(w, sid)
	if sess == nil {
		return
	}
	ac, ok := sess.reg.get(callID)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such call"})
		return
	}

	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		Subprotocols:       []string{"pcm16"},
		InsecureSkipVerify: true, // CORS já tratado pelo withCORS; origem validada pela API key
	})
	if err != nil {
		s.log.Warn("ws_bridge: upgrade failed", "err", err, "sid", sid, "call", callID)
		return
	}

	events := r.URL.Query().Get("events") == "1" || r.URL.Query().Get("events") == "true"
	bridge := newWSBridge(conn, s.log, events)

	// uplink: PCM16 16 kHz do consumidor → grava + injeta na chamada (codec MLow
	// do WhatsApp). Sem Opus intermediário.
	bridge.OnBrowserPCM = func(pcm16 []float32) {
		ac.recorder.writeBrowser(pcm16)
		ac.cm.FeedCapturedPCM(pcm16)
	}
	bridge.OnTerminalWS = func() {
		go sess.terminateCall(callID, core.EndCallReasonUserEnded)
	}

	// Registra o bridge WebSocket na chamada (fecha um transporte anterior se havia).
	// Sem codec Opus: o downlink sai por WritePCM (ver Session.OnPeerAudio).
	sess.setWSBridge(callID, bridge, nil)

	s.log.Info("ws_bridge: connected", "sid", sid, "call", callID, "events", events)

	go bridge.keepAlive()
	bridge.readLoop() // bloqueia até o WS fechar

	s.log.Info("ws_bridge: disconnected", "sid", sid, "call", callID)
}

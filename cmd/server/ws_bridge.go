package main

import (
	"context"
	"encoding/binary"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"
	"wacalls/internal/voip/core"
	"wacalls/internal/voip/media"
)

// wsBridge substitui a ponte pion/WebRTC para clientes que passam por proxy
// reverso HTTP (Cloudflare, Nginx, Traefik…), onde UDP é bloqueado.
//
// O browser captura o microfone via Web Audio API, converte para PCM Int16 LE
// 16 kHz e envia os frames binários via WebSocket sobre WSS/443 — que atravessa
// qualquer proxy reverso. O áudio do peer (WhatsApp → browser) vem pelo mesmo
// caminho na direção inversa.
type wsBridge struct {
	conn        *websocket.Conn
	ctx         context.Context
	cancel      context.CancelFunc
	mu          sync.Mutex
	closed      bool
	log         *slog.Logger
	browserOpus media.Codec

	// Callbacks — mesma semântica de Bridge
	OnBrowserRTP func(payload []byte) // uplink PCM já convertido em Opus
	OnTerminalWS func()               // disparado ao fechar o WS
}

func newWSBridge(conn *websocket.Conn, log *slog.Logger) *wsBridge {
	ctx, cancel := context.WithCancel(context.Background())
	return &wsBridge{conn: conn, ctx: ctx, cancel: cancel, log: log}
}

// WriteOpus recebe Opus do peer (downlink: WhatsApp→browser), decodifica para
// PCM Float32, converte para Int16 LE e envia pelo WebSocket.
func (b *wsBridge) WriteOpus(opus []byte, _ time.Duration) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed || b.browserOpus == nil {
		return nil
	}
	pcm48, err := b.browserOpus.Decode(opus)
	if err != nil || len(pcm48) == 0 {
		return nil
	}
	pcm16f := media.Downsample48to16(pcm48)
	buf := make([]byte, len(pcm16f)*2)
	for i, v := range pcm16f {
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
	return b.conn.Write(b.ctx, websocket.MessageBinary, buf)
}

// WriteVideo é no-op: WebSocket audio bridge não suporta vídeo.
func (b *wsBridge) WriteVideo(_ []byte) error { return nil }

// DisableTerminate evita que o fechamento dispare OnTerminalWS (usado em
// renegociação/transferência, igual ao comportamento de Bridge.DisableTerminate).
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

// readLoop lê frames PCM Int16 LE 16 kHz do browser (uplink), codifica em Opus
// e entrega ao CallManager via OnBrowserRTP.
// Bloqueia até o WebSocket fechar.
func (b *wsBridge) readLoop(browserOpus media.Codec) {
	b.mu.Lock()
	b.browserOpus = browserOpus
	b.mu.Unlock()

	// Encoder Opus para uplink (browser PCM → MLow no CallManager)
	opusEnc, err := media.NewOpusCodec(48000, 960)
	if err != nil {
		b.log.Warn("ws_bridge: opus encoder unavailable — uplink disabled", "err", err)
	}

	for {
		mt, data, err := b.conn.Read(b.ctx)
		if err != nil {
			break
		}
		if mt != websocket.MessageBinary || len(data) == 0 {
			continue
		}
		if b.OnBrowserRTP == nil || opusEnc == nil {
			continue
		}
		// PCM Int16 LE 16 kHz → float32 16 kHz
		samples := make([]float32, len(data)/2)
		for i := range samples {
			iv := int16(binary.LittleEndian.Uint16(data[i*2:]))
			samples[i] = float32(iv) / 32768.0
		}
		// upsample 16 kHz → 48 kHz para o encoder Opus
		pcm48 := media.Upsample16to48(samples)
		opus, err := opusEnc.Encode(pcm48)
		if err != nil || len(opus) == 0 {
			continue
		}
		b.OnBrowserRTP(opus)
	}

	if opusEnc != nil {
		opusEnc.Close()
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

	bridge := newWSBridge(conn, s.log)

	browserOpus, ocErr := media.NewOpusCodec(48000, 960)
	if ocErr != nil {
		s.log.Warn("ws_bridge: browser Opus codec unavailable — call audio disabled", "err", ocErr)
		browserOpus = nil
	}

	bridge.OnBrowserRTP = func(payload []byte) {
		if browserOpus == nil {
			return
		}
		pcm48, err := browserOpus.Decode(payload)
		if err != nil {
			return
		}
		down := media.Downsample48to16(pcm48)
		ac.recorder.writeBrowser(down)
		ac.cm.FeedCapturedPCM(down)
	}
	bridge.OnTerminalWS = func() {
		go sess.terminateCall(callID, core.EndCallReasonUserEnded)
	}

	// Registra o bridge WebSocket na chamada (fecha WebRTC anterior se existia)
	sess.setWSBridge(callID, bridge, browserOpus)

	s.log.Info("ws_bridge: connected", "sid", sid, "call", callID)

	go bridge.keepAlive()
	bridge.readLoop(browserOpus) // bloqueia até o WS fechar

	s.log.Info("ws_bridge: disconnected", "sid", sid, "call", callID)
}

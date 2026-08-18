package main

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func wsTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func wsDialURL(t *testing.T, srv *httptest.Server) (*websocket.Conn, context.Context, context.CancelFunc) {
	t.Helper()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	conn, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{Subprotocols: []string{"pcm16"}})
	if err != nil {
		cancel()
		t.Fatalf("dial: %v", err)
	}
	return conn, ctx, cancel
}

// TestWSBridgeUplinkPCM: bytes PCM16 LE do cliente chegam ao OnBrowserPCM como
// float32 [-1,1] — o caminho que injeta o áudio do consumidor na chamada.
func TestWSBridgeUplinkPCM(t *testing.T) {
	got := make(chan []float32, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{"pcm16"}})
		if err != nil {
			return
		}
		b := newWSBridge(conn, wsTestLogger(), false)
		b.OnBrowserPCM = func(pcm []float32) { got <- pcm }
		b.readLoop()
	}))
	defer srv.Close()

	conn, ctx, cancel := wsDialURL(t, srv)
	defer cancel()

	in := []int16{0, 16384, -16384}
	buf := make([]byte, len(in)*2)
	for i, v := range in {
		binary.LittleEndian.PutUint16(buf[i*2:], uint16(v))
	}
	if err := conn.Write(ctx, websocket.MessageBinary, buf); err != nil {
		t.Fatalf("write: %v", err)
	}

	select {
	case pcm := <-got:
		if len(pcm) != 3 {
			t.Fatalf("len=%d want 3", len(pcm))
		}
		if pcm[0] != 0 {
			t.Errorf("pcm[0]=%f want 0", pcm[0])
		}
		if pcm[1] < 0.49 || pcm[1] > 0.51 {
			t.Errorf("pcm[1]=%f want ~0.5", pcm[1])
		}
		if pcm[2] > -0.49 || pcm[2] < -0.51 {
			t.Errorf("pcm[2]=%f want ~-0.5", pcm[2])
		}
	case <-ctx.Done():
		t.Fatal("timeout esperando OnBrowserPCM")
	}
	_ = conn.Close(websocket.StatusNormalClosure, "")
}

// TestWSBridgeDownlinkAndEvents: WritePCM entrega áudio binário PCM16 ao cliente,
// e SendEvent entrega um frame de TEXTO JSON quando o socket foi aberto com
// events=true. Prova o full-duplex + o canal de eventos no mesmo socket.
func TestWSBridgeDownlinkAndEvents(t *testing.T) {
	bridgeCh := make(chan *wsBridge, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{"pcm16"}})
		if err != nil {
			return
		}
		b := newWSBridge(conn, wsTestLogger(), true) // events ligado
		bridgeCh <- b
		b.readLoop() // mantém a conexão aberta
	}))
	defer srv.Close()

	conn, ctx, cancel := wsDialURL(t, srv)
	defer cancel()

	b := <-bridgeCh
	b.SendEvent(map[string]any{"type": "call-ended", "reason": "test"})
	if err := b.WritePCM([]float32{0.5, -0.5}); err != nil {
		t.Fatalf("WritePCM: %v", err)
	}

	// frame 1: texto JSON (evento)
	mt, data, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("read evento: %v", err)
	}
	if mt != websocket.MessageText {
		t.Fatalf("frame 1 mt=%v want Text", mt)
	}
	var ev map[string]any
	if err := json.Unmarshal(data, &ev); err != nil {
		t.Fatalf("json: %v", err)
	}
	if ev["type"] != "call-ended" || ev["reason"] != "test" {
		t.Errorf("evento inesperado: %v", ev)
	}

	// frame 2: áudio binário PCM16
	mt, data, err = conn.Read(ctx)
	if err != nil {
		t.Fatalf("read audio: %v", err)
	}
	if mt != websocket.MessageBinary {
		t.Fatalf("frame 2 mt=%v want Binary", mt)
	}
	if len(data) != 4 {
		t.Fatalf("audio len=%d want 4", len(data))
	}
	s0 := int16(binary.LittleEndian.Uint16(data[0:]))
	s1 := int16(binary.LittleEndian.Uint16(data[2:]))
	if s0 < 16300 || s0 > 16384 { // 0.5*32767 = 16383
		t.Errorf("s0=%d want ~16383", s0)
	}
	if s1 > -16300 || s1 < -16400 { // -0.5*32768 = -16384
		t.Errorf("s1=%d want ~-16384", s1)
	}
	_ = conn.Close(websocket.StatusNormalClosure, "")
}

// TestWSBridgeEventsOff: sem events=true, SendEvent é no-op — o cliente recebe
// direto o áudio binário, sem frame de texto na frente (o painel/widget não quer
// eventos JSON no socket de áudio).
func TestWSBridgeEventsOff(t *testing.T) {
	bridgeCh := make(chan *wsBridge, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{"pcm16"}})
		if err != nil {
			return
		}
		b := newWSBridge(conn, wsTestLogger(), false) // events desligado
		bridgeCh <- b
		b.readLoop()
	}))
	defer srv.Close()

	conn, ctx, cancel := wsDialURL(t, srv)
	defer cancel()

	b := <-bridgeCh
	b.SendEvent(map[string]any{"type": "call-status", "status": "connected"}) // deve ser ignorado
	if err := b.WritePCM([]float32{0.25}); err != nil {
		t.Fatalf("WritePCM: %v", err)
	}

	mt, _, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if mt != websocket.MessageBinary {
		t.Fatalf("primeiro frame mt=%v want Binary (evento não devia ter sido enviado)", mt)
	}
	_ = conn.Close(websocket.StatusNormalClosure, "")
}

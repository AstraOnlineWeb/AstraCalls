package main

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/coder/websocket"
	"wacalls/internal/voip/call"
)

// fakeBrowserOpus é um codec Opus mínimo para testes sem depender de libopus.
type fakeBrowserOpus struct{}

func (fakeBrowserOpus) Encode([]float32) ([]byte, error) { return []byte{0xF8, 0xFF, 0xFE}, nil }
func (fakeBrowserOpus) Decode([]byte) ([]float32, error) { return nil, nil }
func (fakeBrowserOpus) FrameSize() int                   { return 960 }
func (fakeBrowserOpus) SampleRate() int                  { return 48000 }
func (fakeBrowserOpus) Close()                           {}

// fakeWSOpus simula o codec do lado WS: decodifica qualquer frame em um buffer
// PCM não-vazio para que o WriteOpus envie bytes pelo WebSocket.
type fakeWSOpus struct{}

func (fakeWSOpus) Encode([]float32) ([]byte, error) { return []byte{0xF8, 0xFF, 0xFE}, nil }
func (fakeWSOpus) Decode([]byte) ([]float32, error) {
	return []float32{0.1, 0.2, 0.3, 0.4, 0.5, 0.6}, nil
}
func (fakeWSOpus) FrameSize() int  { return 960 }
func (fakeWSOpus) SampleRate() int { return 48000 }
func (fakeWSOpus) Close()          {}

// wsBridgeConn cria um par de conexões websocket para teste.
func wsBridgeConn(t *testing.T) (*websocket.Conn, *websocket.Conn, func()) {
	t.Helper()
	var serverConn *websocket.Conn
	var serverErr error
	var done = make(chan struct{})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		serverConn, serverErr = websocket.Accept(w, r, &websocket.AcceptOptions{
			Subprotocols: []string{"pcm16"},
		})
		close(done)
	}))

	clientConn, _, err := websocket.Dial(context.Background(), "ws"+srv.URL[4:], &websocket.DialOptions{
		Subprotocols: []string{"pcm16"},
	})
	if err != nil {
		t.Fatalf("dial ws: %v", err)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("timeout esperando accept websocket")
	}
	if serverErr != nil {
		t.Fatalf("accept ws: %v", serverErr)
	}

	cleanup := func() {
		_ = clientConn.Close(websocket.StatusNormalClosure, "")
		_ = serverConn.Close(websocket.StatusNormalClosure, "")
		srv.Close()
	}
	return serverConn, clientConn, cleanup
}

// TestPeerAudioFlowsToWSBridgeWhenWebRTCDisabled garante que, quando só a ponte
// WS está disponível (ac.bridge == nil), o áudio do peer ainda é entregue ao
// navegador. Esse era exatamente o cenário do áudio mudo outbound.
func TestPeerAudioFlowsToWSBridgeWhenWebRTCDisabled(t *testing.T) {
	serverConn, clientConn, cleanup := wsBridgeConn(t)
	defer cleanup()
	wsb := newWSBridge(serverConn, testLogger(t))
	wsb.browserOpus = fakeWSOpus{}

	s := &Session{id: "s1", reg: newCallRegistry()}
	cm := call.NewCallManager(nil, testLogger(t))
	s.wireCall(cm, "CID")
	ac := &activeCall{cm: cm, browserOpus: fakeBrowserOpus{}}
	s.reg.add("CID", ac)

	ac.bridge = nil
	ac.wsBridge = wsb

	cm.OnPeerAudio([]float32{0.0, 0.5, -0.5})

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, data, err := clientConn.Read(ctx)
	if err != nil {
		t.Fatalf("áudio do peer não chegou à ponte WebSocket: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("ponte WebSocket recebeu payload vazio")
	}
}

// TestPeerAudioFlowsToWSBridgeAlongsideWebRTC garante que, durante a transição
// entre pontes (WebRTC ainda registrado e WS já ativo), o áudio do peer continua
// sendo entregue pelo fallback WS. Regressão do áudio mudo no fallback WS.
func TestPeerAudioFlowsToWSBridgeAlongsideWebRTC(t *testing.T) {
	offer := makeBrowserOffer(t)
	br, _, err := NewBridge(offer, testLogger(t))
	if err != nil {
		t.Fatalf("NewBridge failed: %v", err)
	}
	defer br.Close()

	serverConn, clientConn, cleanup := wsBridgeConn(t)
	defer cleanup()
	wsb := newWSBridge(serverConn, testLogger(t))
	wsb.browserOpus = fakeWSOpus{}

	s := &Session{id: "s1", reg: newCallRegistry()}
	cm := call.NewCallManager(nil, testLogger(t))
	s.wireCall(cm, "CID")
	ac := &activeCall{cm: cm, browserOpus: fakeBrowserOpus{}}
	s.reg.add("CID", ac)

	// Ambas as pontes registradas simultaneamente.
	ac.bridge = br
	ac.wsBridge = wsb

	cm.OnPeerAudio([]float32{0.0, 0.5, -0.5})

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, data, err := clientConn.Read(ctx)
	if err != nil {
		t.Fatalf("áudio do peer não chegou à ponte WebSocket: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("ponte WebSocket recebeu payload vazio")
	}
}

// TestPeerAudioDoesNotCrashWithoutBrowserOpus garante que, se o codec do
// navegador não está disponível, o callback de áudio do peer retorna silenciosamente.
func TestPeerAudioDoesNotCrashWithoutBrowserOpus(t *testing.T) {
	s := &Session{id: "s1", reg: newCallRegistry()}
	cm := call.NewCallManager(nil, testLogger(t))
	s.wireCall(cm, "CID")
	ac := &activeCall{cm: cm}
	s.reg.add("CID", ac)

	cm.OnPeerAudio([]float32{0.0, 0.5, -0.5})
}

func testLogger(t *testing.T) *slog.Logger {
	t.Helper()
	return slog.Default()
}

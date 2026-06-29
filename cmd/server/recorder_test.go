package main

import (
	"encoding/binary"
	"log/slog"
	"math"
	"testing"
	"time"
)

func TestMixFrameInto_PositionsByArrivalTime(t *testing.T) {
	// Um frame que chega em 1s, com 320 amostras (20 ms @ 16k), deve começar em
	// 16000 - 320 = 15680.
	buf := mixFrameInto(nil, time.Second, make([]float32, 320))
	if len(buf) != 16000 {
		t.Fatalf("expected buffer len 16000, got %d", len(buf))
	}
	// Antes do frame: silêncio.
	if buf[15679] != 0 {
		t.Fatalf("expected silence before frame, got %f at 15679", buf[15679])
	}
}

func TestMixFrameInto_SumsOverlappingSides(t *testing.T) {
	a := []float32{0.5, 0.5, 0.5}
	b := []float32{0.25, 0.25, 0.25}
	// Mesma marca de tempo → mesma posição → soma.
	at := time.Duration(len(a)) * time.Second / recSampleRate
	buf := mixFrameInto(nil, at, a)
	buf = mixFrameInto(buf, at, b)
	for i := 0; i < 3; i++ {
		if math.Abs(float64(buf[i]-0.75)) > 1e-6 {
			t.Fatalf("expected mixed 0.75 at %d, got %f", i, buf[i])
		}
	}
}

func TestMixFrameInto_ClampsToRange(t *testing.T) {
	at := time.Duration(2) * time.Second / recSampleRate
	buf := mixFrameInto(nil, at, []float32{0.8, -0.8})
	buf = mixFrameInto(buf, at, []float32{0.8, -0.8})
	if buf[0] != 1 {
		t.Fatalf("expected clamp to +1, got %f", buf[0])
	}
	if buf[1] != -1 {
		t.Fatalf("expected clamp to -1, got %f", buf[1])
	}
}

func TestRecorder_DrainsBothSides(t *testing.T) {
	r := newCallRecorder("TESTCALL", slog.Default(), time.Now().Add(-2*time.Second))
	// Frames chegando ~agora (start foi 2s atrás), então caem perto de 32000.
	r.writePeer(make([]float32, 320))
	r.writeBrowser(make([]float32, 320))
	r.closeFrames()
	<-r.doneMix
	if len(r.mixed) == 0 {
		t.Fatal("expected mixed audio after draining frames")
	}
}

func TestRecorder_WriteAfterCloseDoesNotPanic(t *testing.T) {
	r := newCallRecorder("RACECALL", slog.Default(), time.Now())
	r.writePeer(make([]float32, 160))
	r.closeFrames()
	<-r.doneMix
	// Writes concorrentes/atrasados após o fechamento devem ser no-ops, não panic.
	for i := 0; i < 50; i++ {
		r.writePeer(make([]float32, 160))
		r.writeBrowser(make([]float32, 160))
	}
	r.closeFrames() // idempotente
}

func TestFloat32ToLE_RoundTrip(t *testing.T) {
	in := []float32{0, 0.5, -0.5, 1, -1}
	raw := float32ToLE(in)
	if len(raw) != len(in)*4 {
		t.Fatalf("expected %d bytes, got %d", len(in)*4, len(raw))
	}
	for i, want := range in {
		got := math.Float32frombits(binary.LittleEndian.Uint32(raw[i*4:]))
		if got != want {
			t.Fatalf("sample %d: want %f, got %f", i, want, got)
		}
	}
}

func TestSafeRecordingID(t *testing.T) {
	ok := []string{"ABC123.mp3", "a1b2c3.mp3", "deadBEEF", "x-y_z.mp3"}
	for _, id := range ok {
		if !safeRecordingID(id) {
			t.Errorf("expected %q to be safe", id)
		}
	}
	bad := []string{"", ".", "..", "../etc/passwd", "a/b.mp3", "foo..bar", "a b.mp3", "x;rm.mp3"}
	for _, id := range bad {
		if safeRecordingID(id) {
			t.Errorf("expected %q to be rejected", id)
		}
	}
}

func TestRecordingPublicURL(t *testing.T) {
	t.Setenv("WACALLS_PUBLIC_BASE_URL", "https://voice.example.com/")
	got := recordingPublicURL("/tmp/wacalls-recordings/ABC123.mp3")
	want := "https://voice.example.com/recordings/ABC123.mp3"
	if got != want {
		t.Fatalf("want %q, got %q", want, got)
	}
	t.Setenv("WACALLS_PUBLIC_BASE_URL", "")
	if recordingPublicURL("/tmp/x.mp3") != "" {
		t.Fatal("expected empty URL when base not configured")
	}
}

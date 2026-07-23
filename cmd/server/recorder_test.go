package main

import (
	"encoding/binary"
	"log/slog"
	"math"
	"testing"
	"time"
)

func TestMixFrameInto_PositionsByIndex(t *testing.T) {
	// Um frame de 320 amostras (20 ms @ 16k) posicionado em 15680 deve deixar
	// silêncio antes e terminar o buffer em 16000.
	buf := mixFrameInto(nil, 15680, make([]float32, 320))
	if len(buf) != 16000 {
		t.Fatalf("expected buffer len 16000, got %d", len(buf))
	}
	if buf[15679] != 0 {
		t.Fatalf("expected silence before frame, got %f at 15679", buf[15679])
	}
}

func TestMixFrameInto_SumsOverlappingSides(t *testing.T) {
	a := []float32{0.5, 0.5, 0.5}
	b := []float32{0.25, 0.25, 0.25}
	buf := mixFrameInto(nil, 0, a)
	buf = mixFrameInto(buf, 0, b)
	for i := 0; i < 3; i++ {
		if math.Abs(float64(buf[i]-0.75)) > 1e-6 {
			t.Fatalf("expected mixed 0.75 at %d, got %f", i, buf[i])
		}
	}
}

func TestMixFrameInto_ClampsToRange(t *testing.T) {
	buf := mixFrameInto(nil, 2, []float32{0.8, -0.8})
	buf = mixFrameInto(buf, 2, []float32{0.8, -0.8})
	if buf[2] != 1 {
		t.Fatalf("expected clamp to +1, got %f", buf[2])
	}
	if buf[3] != -1 {
		t.Fatalf("expected clamp to -1, got %f", buf[3])
	}
}

// frame20ms devolve um frame de 20 ms @ 16k preenchido com o valor dado.
func frame20ms(v float32) []float32 {
	pcm := make([]float32, 320)
	for i := range pcm {
		pcm[i] = v
	}
	return pcm
}

func TestPlace_JitterDoesNotCreateGapsNorOverlap(t *testing.T) {
	// Cinco frames contíguos do mesmo lado chegando com jitter (fora da grade
	// de 20 ms) devem sair emendados: sem zeros entre eles e sem soma
	// sobreposta — exatamente 5×320 amostras com o valor original.
	r := &callRecorder{}
	for _, at := range []time.Duration{
		20 * time.Millisecond,
		55 * time.Millisecond, // atrasado 15 ms
		61 * time.Millisecond, // rajada logo atrás
		95 * time.Millisecond,
		130 * time.Millisecond,
	} {
		r.place(recFrame{side: sidePeer, at: at, pcm: frame20ms(0.5)})
	}
	if len(r.mixed) != 5*320 {
		t.Fatalf("expected %d samples, got %d", 5*320, len(r.mixed))
	}
	for i, s := range r.mixed {
		if math.Abs(float64(s-0.5)) > 1e-6 {
			t.Fatalf("expected seamless 0.5 at %d, got %f (gap or overlap)", i, s)
		}
	}
}

func TestPlace_ResyncKeepsRealSilence(t *testing.T) {
	// Um vão real (mute/perda prolongada) acima do limiar deve reancorar no
	// relógio: o silêncio permanece na gravação.
	r := &callRecorder{}
	r.place(recFrame{side: sidePeer, at: 20 * time.Millisecond, pcm: frame20ms(0.5)})
	r.place(recFrame{side: sidePeer, at: 2 * time.Second, pcm: frame20ms(0.5)})
	wantLen := 2 * recSampleRate // reancorado: termina no instante de chegada
	if len(r.mixed) != wantLen {
		t.Fatalf("expected %d samples, got %d", wantLen, len(r.mixed))
	}
	if r.mixed[len(r.mixed)/2] != 0 {
		t.Fatal("expected silence preserved inside the real gap")
	}
}

func TestPlace_TrimsLeadingSetupSilence(t *testing.T) {
	// O toque/setup antes do primeiro frame não deve virar silêncio no arquivo.
	r := &callRecorder{}
	r.place(recFrame{side: sidePeer, at: 11 * time.Second, pcm: frame20ms(0.5)})
	if len(r.mixed) != 320 {
		t.Fatalf("expected 320 samples (no leading silence), got %d", len(r.mixed))
	}
}

func TestPlace_SidesStayAligned(t *testing.T) {
	// O navegador conecta 0.5s depois do peer: a defasagem relativa entre os
	// lados precisa sobreviver ao corte do silêncio inicial.
	r := &callRecorder{}
	r.place(recFrame{side: sidePeer, at: time.Second, pcm: frame20ms(0.5)})
	r.place(recFrame{side: sideBrowser, at: 1500 * time.Millisecond, pcm: frame20ms(0.25)})
	wantLen := 8000 + 320 // browser em +0.5s relativos
	if len(r.mixed) != wantLen {
		t.Fatalf("expected %d samples, got %d", wantLen, len(r.mixed))
	}
	if math.Abs(float64(r.mixed[0]-0.5)) > 1e-6 {
		t.Fatalf("expected peer at t=0, got %f", r.mixed[0])
	}
	if math.Abs(float64(r.mixed[8000]-0.25)) > 1e-6 {
		t.Fatalf("expected browser at +0.5s, got %f", r.mixed[8000])
	}
}

func TestRecorder_DrainsBothSides(t *testing.T) {
	r := newCallRecorder("TESTCALL", slog.Default(), time.Now().Add(-2*time.Second))
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

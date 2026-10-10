package main

import (
	"sync"
	"testing"
	"time"
)

// fakeSpySink acumula o que o hub entrega.
type fakeSpySink struct {
	mu     sync.Mutex
	frames [][]float32
	closed bool
}

func (f *fakeSpySink) WritePCM(pcm []float32) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := append([]float32(nil), pcm...)
	f.frames = append(f.frames, cp)
	return nil
}
func (f *fakeSpySink) Close() { f.mu.Lock(); f.closed = true; f.mu.Unlock() }
func (f *fakeSpySink) n() int { f.mu.Lock(); defer f.mu.Unlock(); return len(f.frames) }

func constFrame(v float32, n int) []float32 {
	out := make([]float32, n)
	for i := range out {
		out[i] = v
	}
	return out
}

// Sem espião, feed é no-op (nada fica enfileirado) e o laço de mix não roda.
func TestSpyHubNoListenersIsNoop(t *testing.T) {
	h := newSpyHub()
	h.feed(spySidePeer, constFrame(0.5, 960))
	if _, ok := h.mixer.MixChunk(); ok {
		t.Fatal("áudio enfileirado sem espião")
	}
	if h.count() != 0 || len(h.ids()) != 0 {
		t.Fatal("contagem errada sem espião")
	}
}

// Com um espião, o mix (peer + atendente) chega em frames de 960 amostras, somado.
func TestSpyHubMixesBothSides(t *testing.T) {
	h := newSpyHub()
	var changes []int
	h.OnChange = func(n int) { changes = append(changes, n) }
	sink := &fakeSpySink{}
	id := h.add(sink)
	if id == "" || h.count() != 1 {
		t.Fatalf("add: id=%q count=%d", id, h.count())
	}
	// 10 frames de 60ms de cada lado (bem acima do prefill do mixer)
	for i := 0; i < 10; i++ {
		h.feed(spySidePeer, constFrame(0.25, 960))
		h.feed(spySideAgent, constFrame(0.25, 960))
	}
	deadline := time.Now().Add(2 * time.Second)
	for sink.n() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if sink.n() == 0 {
		t.Fatal("espião não recebeu nenhum frame")
	}
	sink.mu.Lock()
	fr := sink.frames[0]
	sink.mu.Unlock()
	if len(fr) != 960 {
		t.Fatalf("frame de %d amostras; esperado 960", len(fr))
	}
	// soma dos dois lados: 0.25 + 0.25 = 0.5
	if fr[100] < 0.49 || fr[100] > 0.51 {
		t.Fatalf("mix = %v; esperado ~0.5 (peer+agente)", fr[100])
	}
	if !h.remove(id) {
		t.Fatal("remove devolveu false")
	}
	if h.remove(id) {
		t.Fatal("remove repetido devolveu true")
	}
	if !sink.closed || h.count() != 0 {
		t.Fatal("sink não fechado / contagem não zerou")
	}
	if len(changes) != 2 || changes[0] != 1 || changes[1] != 0 {
		t.Fatalf("OnChange = %v; esperado [1 0]", changes)
	}
}

// removeSink pelo ponteiro (callback de fechamento do transporte) e closeAll.
func TestSpyHubRemoveSinkAndCloseAll(t *testing.T) {
	h := newSpyHub()
	a, b := &fakeSpySink{}, &fakeSpySink{}
	h.add(a)
	h.add(b)
	h.removeSink(a)
	h.removeSink(a) // idempotente
	if !a.closed || b.closed || h.count() != 1 {
		t.Fatal("removeSink errado")
	}
	h.closeAll()
	if !b.closed || h.count() != 0 {
		t.Fatal("closeAll não fechou tudo")
	}
	var nilHub *spyHub
	nilHub.feed(spySidePeer, constFrame(1, 10)) // nil-safe
	nilHub.closeAll()
	nilHub.removeSink(a)
}

func TestApplyGainAndVolume(t *testing.T) {
	in := []float32{0.5, -0.5, 0.9}
	if out := applyGain(in, 1); &out[0] != &in[0] {
		t.Fatal("ganho 1 deveria devolver o mesmo slice")
	}
	out := applyGain(in, 2)
	if out[0] != 1 || out[1] != -1 || out[2] != 1 {
		t.Fatalf("clamp errado: %v", out)
	}
	out = applyGain(in, 0.5)
	if out[0] != 0.25 || out[1] != -0.25 {
		t.Fatalf("ganho 0.5 errado: %v", out)
	}
	if out = applyGain(in, 0); out[0] != 0 || out[2] != 0 {
		t.Fatalf("mudo errado: %v", out)
	}

	ac := &activeCall{}
	if ac.volumeLevel() != 1 {
		t.Fatal("padrão deveria ser 1.0")
	}
	if got := ac.setVolume(0); got != 0 || ac.volumeLevel() != 0 {
		t.Fatalf("volume 0 (mudo) não guardado: %v / %v", got, ac.volumeLevel())
	}
	if got := ac.setVolume(1.5); got != 1.5 || ac.volumeLevel() != 1.5 {
		t.Fatalf("volume 1.5: %v / %v", got, ac.volumeLevel())
	}
	if got := ac.setVolume(99); got != volumeMax {
		t.Fatalf("clamp máximo: %v", got)
	}
	if got := ac.setVolume(-3); got != 0 {
		t.Fatalf("clamp mínimo: %v", got)
	}
}

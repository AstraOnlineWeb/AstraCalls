package groupmix

import "testing"

func constPCM(n int, v float32) []float32 {
	s := make([]float32, n)
	for i := range s {
		s[i] = v
	}
	return s
}

func TestMixerSumsTwoParticipants(t *testing.T) {
	m := NewMixer()
	// Enfileira o bastante pra passar o prefill (2*960) + 1 chunk (160).
	m.Add("a", constPCM(prefillSamples+chunkSamples, 0.2))
	m.Add("b", constPCM(prefillSamples+chunkSamples, 0.3))

	chunk, ok := m.MixChunk()
	if !ok {
		t.Fatalf("MixChunk não produziu nada")
	}
	if len(chunk) != chunkSamples {
		t.Fatalf("len chunk = %d, quer %d", len(chunk), chunkSamples)
	}
	want := float32(0.5) // 0.2 + 0.3
	if chunk[0] < want-1e-4 || chunk[0] > want+1e-4 {
		t.Errorf("soma = %v, quer ~%v", chunk[0], want)
	}
}

func TestMixerClamps(t *testing.T) {
	m := NewMixer()
	m.Add("a", constPCM(prefillSamples+chunkSamples, 0.8))
	m.Add("b", constPCM(prefillSamples+chunkSamples, 0.8))
	chunk, ok := m.MixChunk()
	if !ok {
		t.Fatal("sem chunk")
	}
	if chunk[0] != 1.0 {
		t.Errorf("clamp: %v, quer 1.0", chunk[0])
	}
}

func TestMixerPrefill(t *testing.T) {
	m := NewMixer()
	// Menos que o prefill: não deve contribuir ainda.
	m.Add("a", constPCM(chunkSamples, 0.5))
	if _, ok := m.MixChunk(); ok {
		t.Errorf("deveria aguardar prefill antes de mixar")
	}
}

func TestRetainDropsDeparted(t *testing.T) {
	m := NewMixer()
	m.Add("a", constPCM(prefillSamples+chunkSamples, 0.4))
	m.Add("b", constPCM(prefillSamples+chunkSamples, 0.4))
	m.Retain([]string{"a"}) // b saiu
	chunk, ok := m.MixChunk()
	if !ok {
		t.Fatal("sem chunk")
	}
	if chunk[0] < 0.4-1e-4 || chunk[0] > 0.4+1e-4 {
		t.Errorf("só 'a' deveria contribuir: %v", chunk[0])
	}
}

func TestFramerReframes(t *testing.T) {
	var f Framer
	// 6 chunks de 160 = 960 -> 1 frame.
	var got [][]float32
	for i := 0; i < 6; i++ {
		if frame, ok := f.Push(constPCM(chunkSamples, 0.1)); ok {
			got = append(got, frame)
		}
	}
	if len(got) != 1 || len(got[0]) != frameSamples {
		t.Fatalf("frames = %d (esperado 1 de %d)", len(got), frameSamples)
	}
}

func TestShouldMix(t *testing.T) {
	if ShouldMix([]string{"a"}) {
		t.Error("1 participante não deve mixar")
	}
	if !ShouldMix([]string{"a", "b"}) {
		t.Error("2 participantes devem mixar")
	}
}

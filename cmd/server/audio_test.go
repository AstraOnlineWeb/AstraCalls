package main

import (
	"encoding/binary"
	"testing"
)

func TestSynthWaveform(t *testing.T) {
	wf := synthWaveform()
	if len(wf) != 64 {
		t.Fatalf("waveform deve ter 64 bytes, tem %d", len(wf))
	}
	allZero := true
	for _, v := range wf {
		if v > 100 {
			t.Fatalf("amostra fora da faixa 0-100: %d", v)
		}
		if v != 0 {
			allZero = false
		}
	}
	if allZero {
		t.Error("waveform sintético não deveria ser todo zero (barra reta)")
	}
}

// buildOggOpus monta um OGG/Opus mínimo: um OpusHead com o pre-skip informado e
// uma página OggS carregando a granule position dada.
func buildOggOpus(preSkip uint16, granule uint64) []byte {
	var head []byte
	head = append(head, []byte("OpusHead")...)
	head = append(head, 1, 1) // versão, canais
	ps := make([]byte, 2)
	binary.LittleEndian.PutUint16(ps, preSkip)
	head = append(head, ps...) // pre-skip em +10..11

	page := []byte("OggS")
	page = append(page, 0, 0) // versão, header type
	g := make([]byte, 8)
	binary.LittleEndian.PutUint64(g, granule)
	page = append(page, g...)
	page = append(page, make([]byte, 20)...) // resto do cabeçalho (padding)

	return append(head, page...)
}

func TestOggOpusDurationSeconds(t *testing.T) {
	preSkip := uint16(3840)
	// 5 s de áudio a 48 kHz + pre-skip
	data := buildOggOpus(preSkip, 5*48000+uint64(preSkip))
	if got := oggOpusDurationSeconds(data); got != 5 {
		t.Errorf("duração = %ds, esperado 5s", got)
	}
	// não-OGG → 0
	if got := oggOpusDurationSeconds([]byte("isto nao e um ogg valido")); got != 0 {
		t.Errorf("não-OGG deveria dar 0, deu %d", got)
	}
	// granule <= pre-skip → 0 (sem áudio útil)
	if got := oggOpusDurationSeconds(buildOggOpus(3840, 1000)); got != 0 {
		t.Errorf("granule < preskip deveria dar 0, deu %d", got)
	}
}

package main

import (
	"bytes"
	"encoding/binary"
	"math"
)

// oggOpusDurationSeconds estima a duração (em segundos, arredondada p/ baixo) de
// um arquivo OGG/Opus lendo a última "granule position" das páginas OGG e
// descontando o pre-skip do OpusHead. O granule do Opus é sempre em amostras de
// 48 kHz. Usado como fallback p/ preencher audioMessage.seconds numa nota de voz
// (PTT) quando o cliente não informa `seconds` — sem isso o WhatsApp mostra a
// barra reta, sem tempo. Retorna 0 quando não consegue determinar (ex.: não é OGG).
func oggOpusDurationSeconds(data []byte) uint32 {
	if len(data) < 14 {
		return 0
	}
	var preSkip uint64
	if h := bytes.Index(data, []byte("OpusHead")); h >= 0 && h+12 <= len(data) {
		preSkip = uint64(binary.LittleEndian.Uint16(data[h+10 : h+12]))
	}
	var last uint64
	found := false
	for off := 0; off+14 <= len(data); {
		p := bytes.Index(data[off:], []byte("OggS"))
		if p < 0 {
			break
		}
		pos := off + p
		if pos+14 > len(data) {
			break
		}
		g := binary.LittleEndian.Uint64(data[pos+6 : pos+14])
		if g != 0xFFFFFFFFFFFFFFFF { // -1 = nenhum pacote termina nesta página
			last = g
			found = true
		}
		off = pos + 4
	}
	if !found || last <= preSkip {
		return 0
	}
	return uint32((last - preSkip) / 48000)
}

// synthWaveform gera um waveform de 64 bytes (0–100) aproximado, usado só como
// fallback quando o cliente NÃO manda o waveform real da nota de voz. É um padrão
// determinístico (sem áudio decodificado) só para o destinatário ver uma "onda"
// em vez da barra reta. O waveform real, quando enviado no corpo, tem prioridade.
func synthWaveform() []byte {
	const n = 64
	wf := make([]byte, n)
	for i := 0; i < n; i++ {
		// envelope suave (sobe e desce) + oscilação, mantido na faixa ~8..70.
		env := math.Sin(math.Pi * float64(i) / float64(n-1)) // 0..1..0
		osc := 0.5 + 0.5*math.Sin(float64(i)/2.3)
		v := 8 + env*osc*62
		if v < 0 {
			v = 0
		}
		if v > 100 {
			v = 100
		}
		wf[i] = byte(v)
	}
	return wf
}

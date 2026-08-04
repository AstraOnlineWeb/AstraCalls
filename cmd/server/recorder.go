package main

// Motor de gravação de chamada — baseado na contribuição de @Mercantes
// (PR #12, feat/call-recording-webhook). Captura o PCM dos dois lados
// (peer/WhatsApp e navegador), mixa numa trilha mono alinhada no tempo e, ao
// fim da chamada, encoda um MP3. Aqui a gravação é opt-in POR SESSÃO (ver
// Session.recording) e a entrega é via Chatwoot (nota privada) + webhook.

import (
	"bytes"
	"encoding/binary"
	"log/slog"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
)

const (
	recSampleRate = 16000
	// Teto de segurança: 60 min de áudio (16k * 60 * 60 floats ≈ 230 MB). Acima
	// disso paramos de acumular para não estourar memória numa chamada presa.
	recMaxSamples = recSampleRate * 60 * 60
	// Canal de captura: ~5 s de frames de 20 ms por lado. Em sobrecarga a escrita
	// no hot path de mídia descarta o frame em vez de bloquear a ligação ao vivo.
	recChanCap = 512
	// Chamadas muito curtas não têm conteúdo útil — não geram arquivo.
	recMinSeconds = 3
	// Limiar de ressincronização do posicionamento na timeline: dentro dele,
	// frames de um mesmo lado são emendados em sequência (jitter de rede não
	// vira buraco nem sobreposição); acima dele o lado reancora no relógio de
	// chegada (silêncio real: mute, DTX, perda prolongada ou navegador que só
	// conecta depois do início da chamada).
	recResyncSamples = recSampleRate * 3 / 10 // 300 ms
)

// recordingDir é onde os MP3s finalizados ficam até serem baixados/enviados.
func recordingDir() string {
	return envStr("WACALLS_RECORDING_DIR", filepath.Join(os.TempDir(), "wacalls-recordings"))
}

type recSide int

const (
	sidePeer recSide = iota
	sideBrowser
)

type recFrame struct {
	side recSide
	// at = tempo decorrido desde o início da gravação até a chegada do frame.
	at  time.Duration
	pcm []float32
}

// callRecorder acumula e mixa o áudio de uma chamada. Os writes (hot path de
// mídia) só copiam o frame e fazem um send não-bloqueante; toda a mixagem roda
// numa única goroutine, então o buffer mixado não precisa de lock.
type callRecorder struct {
	callID string
	start  time.Time
	log    *slog.Logger

	frames chan recFrame
	mixed  []float32

	// Posicionamento na timeline (só a goroutine de mixagem toca nisso): cada
	// lado tem um cursor de amostras que avança com o próprio áudio, então o
	// stream é emendado contínuo e o jitter de chegada não abre buracos nem
	// sobrepõe frames do mesmo lado. O offset desconta o setup/toque anterior
	// ao primeiro frame gravado — a duração do MP3 passa a bater com a da
	// conversa de fato.
	cursors   [2]int
	cursorSet [2]bool
	offset    int
	offsetSet bool

	// sendMu protege frames contra "send em canal fechado": writes tomam RLock
	// (concorrentes), o fechamento toma Lock (exclusivo) e marca closed.
	sendMu sync.RWMutex
	closed bool

	finishOnce sync.Once
	doneMix    chan struct{}

	finishedPath string
	finishedSecs int
	finishedOK   bool
}

func newCallRecorder(callID string, log *slog.Logger, start time.Time) *callRecorder {
	r := &callRecorder{
		callID:  callID,
		start:   start,
		log:     log,
		frames:  make(chan recFrame, recChanCap),
		doneMix: make(chan struct{}),
	}
	go r.mixLoop()
	return r
}

func (r *callRecorder) writePeer(pcm []float32)    { r.write(sidePeer, pcm) }
func (r *callRecorder) writeBrowser(pcm []float32) { r.write(sideBrowser, pcm) }

// write copia o frame (o chamador reaproveita o buffer) e o enfileira sem
// bloquear. Em sobrecarga o frame é descartado — preservar a ligação ao vivo
// tem prioridade sobre a gravação.
func (r *callRecorder) write(side recSide, pcm []float32) {
	if r == nil || len(pcm) == 0 {
		return
	}
	cp := make([]float32, len(pcm))
	copy(cp, pcm)
	frame := recFrame{side: side, at: time.Since(r.start), pcm: cp}
	r.sendMu.RLock()
	defer r.sendMu.RUnlock()
	if r.closed {
		return
	}
	select {
	case r.frames <- frame:
	default:
		// canal cheio: descarta para não segurar o hot path
	}
}

// closeFrames fecha o canal de captura sob lock exclusivo. Idempotente.
func (r *callRecorder) closeFrames() {
	r.sendMu.Lock()
	defer r.sendMu.Unlock()
	if r.closed {
		return
	}
	r.closed = true
	close(r.frames)
}

// mixLoop é a única goroutine que toca em r.mixed.
func (r *callRecorder) mixLoop() {
	defer close(r.doneMix)
	for frame := range r.frames {
		r.place(frame)
	}
}

// place decide ONDE o frame entra na timeline. O relógio de chegada só ancora o
// primeiro frame de cada lado e os casos em que a deriva do cursor passa do
// limiar (silêncio real); no regime normal cada lado é um stream contínuo e os
// frames são emendados em sequência — o mesmo papel do jitter buffer que o
// navegador e o WhatsApp usam na reprodução ao vivo. Posicionar pelo relógio de
// chegada (comportamento anterior) fazia frame atrasado virar buraco de zeros e
// rajada pós-jitter virar soma sobreposta (distorção/clipping) na gravação.
func (r *callRecorder) place(f recFrame) {
	// O instante de chegada cobre o FIM do frame, então recuamos len(pcm)
	// amostras para achar onde ele começa.
	wall := int(f.at.Seconds()*float64(recSampleRate)) - len(f.pcm)
	if wall < 0 {
		wall = 0
	}
	side := int(f.side)
	idx := r.cursors[side]
	drift := wall - idx
	if !r.cursorSet[side] || drift > recResyncSamples || drift < -recResyncSamples {
		idx = wall
		r.cursorSet[side] = true
	}
	r.cursors[side] = idx + len(f.pcm)
	if !r.offsetSet {
		// O primeiro frame gravado define o zero da timeline: o toque/setup
		// antes de haver áudio não entra como silêncio no arquivo.
		r.offset = idx
		r.offsetSet = true
	}
	rel := idx - r.offset
	if rel < 0 {
		rel = 0 // âncora anterior ao zero (jitter na largada): cola no início
	}
	r.mixed = mixFrameInto(r.mixed, rel, f.pcm)
}

// mixFrameInto soma o frame (com clamp) ao buffer na posição dada, crescendo-o
// conforme necessário. A soma existe para mixar os DOIS lados numa trilha mono;
// sobreposição do mesmo lado não acontece mais (cursor sequencial em place).
func mixFrameInto(buf []float32, startIdx int, pcm []float32) []float32 {
	if startIdx < 0 {
		startIdx = 0
	}
	if startIdx >= recMaxSamples {
		return buf
	}
	end := startIdx + len(pcm)
	if end > recMaxSamples {
		end = recMaxSamples
		pcm = pcm[:end-startIdx]
	}
	if end > len(buf) {
		buf = append(buf, make([]float32, end-len(buf))...)
	}
	for i, s := range pcm {
		v := buf[startIdx+i] + s
		if v > 1 {
			v = 1
		} else if v < -1 {
			v = -1
		}
		buf[startIdx+i] = v
	}
	return buf
}

// finish fecha a captura, espera a mixagem drenar e encoda o MP3. Idempotente.
func (r *callRecorder) finish() (path string, seconds int, ok bool) {
	r.finishOnce.Do(func() {
		r.closeFrames()
		<-r.doneMix

		seconds := len(r.mixed) / recSampleRate
		if seconds < recMinSeconds || len(r.mixed) == 0 {
			r.finishedOK = false
			return
		}

		dir := recordingDir()
		if err := os.MkdirAll(dir, 0o755); err != nil {
			r.log.Warn("recording dir create failed", "err", err)
			r.finishedOK = false
			return
		}
		outPath := filepath.Join(dir, r.callID+".mp3")
		if err := encodeMP3(outPath, r.mixed); err != nil {
			r.log.Warn("recording encode failed", "call_id", r.callID, "err", err)
			r.finishedOK = false
			return
		}
		r.finishedPath = outPath
		r.finishedSecs = seconds
		r.finishedOK = true
	})
	return r.finishedPath, r.finishedSecs, r.finishedOK
}

// encodeMP3 escreve o PCM float32 mono @ 16 kHz num MP3 via ffmpeg (presente no
// runtime — ver Dockerfile). f32le no stdin evita arquivo intermediário.
func encodeMP3(outPath string, pcm []float32) error {
	cmd := exec.Command("ffmpeg", "-y",
		"-f", "f32le", "-ar", "16000", "-ac", "1", "-i", "pipe:0",
		"-c:a", "libmp3lame", "-q:a", "5", outPath)
	cmd.Stdin = bytes.NewReader(float32ToLE(pcm))
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	return cmd.Run()
}

// float32ToLE serializa amostras float32 em little-endian (formato f32le).
func float32ToLE(pcm []float32) []byte {
	buf := make([]byte, len(pcm)*4)
	for i, s := range pcm {
		binary.LittleEndian.PutUint32(buf[i*4:], math.Float32bits(s))
	}
	return buf
}

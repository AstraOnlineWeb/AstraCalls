package main

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

// Gravação de chamadas: captura o PCM dos dois lados (peer/WhatsApp e
// navegador/SDR), mixa numa trilha mono alinhada no tempo e, ao fim da chamada,
// encoda um MP3 servido publicamente. O EnriqueceAI baixa esse MP3 (via webhook
// → recording_url) para transcrever + rodar SPICED. Ver recording_webhook.go.
//
// Ambos os streams são PCM float32 mono @ 16 kHz (OnPeerAudio entrega 16k; o
// áudio do navegador passa por media.Downsample48to16 antes de ser capturado),
// então a mixagem é uma soma direta amostra-a-amostra, sem reamostragem.

const (
	recSampleRate = 16000
	// Teto de segurança: 60 min de áudio (16k * 60 * 60 floats ≈ 230 MB). Acima
	// disso paramos de acumular para não estourar memória numa chamada presa.
	recMaxSamples = recSampleRate * 60 * 60
	// Canal de captura: ~5 s de frames de 20 ms por lado. Em sobrecarga a escrita
	// no hot path de mídia descarta o frame em vez de bloquear a ligação ao vivo.
	recChanCap = 512
	// Chamadas muito curtas não têm conteúdo útil para transcrever — não geram
	// arquivo nem webhook. O app também tem seu próprio piso (TRANSCRIPTION_MIN_*).
	recMinSeconds = 3
)

// recordingDir é onde os MP3s finalizados ficam até o EnriqueceAI baixá-los.
// Efêmero por natureza (o app persiste no próprio bucket logo em seguida).
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

	// sendMu protege frames contra "send em canal fechado": writes tomam RLock
	// (concorrentes), o fechamento toma Lock (exclusivo) e marca closed. Um
	// frame de áudio em voo durante o término não pode causar panic.
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

// closeFrames fecha o canal de captura sob lock exclusivo, garantindo que nenhum
// write esteja a meio de um send (evita panic em canal fechado). Idempotente.
func (r *callRecorder) closeFrames() {
	r.sendMu.Lock()
	defer r.sendMu.Unlock()
	if r.closed {
		return
	}
	r.closed = true
	close(r.frames)
}

// mixLoop é a única goroutine que toca em r.mixed. Cada frame é posicionado pela
// sua marca de tempo de chegada, então os dois lados ficam alinhados na mesma
// timeline (silêncios viram zeros). Frames sobrepostos são somados com clamp.
func (r *callRecorder) mixLoop() {
	defer close(r.doneMix)
	for frame := range r.frames {
		r.mixed = mixFrameInto(r.mixed, frame.at, frame.pcm)
	}
}

// mixFrameInto posiciona um frame na timeline pela sua marca de chegada e o soma
// (com clamp) ao buffer, crescendo-o conforme necessário. Função pura: alinhar
// os dois lados pelo mesmo relógio é o que mantém a conversa sincronizada.
func mixFrameInto(buf []float32, at time.Duration, pcm []float32) []float32 {
	// O instante de chegada cobre o FIM do frame, então recuamos len(pcm)
	// amostras para achar onde ele começa.
	startIdx := int(at.Seconds()*float64(recSampleRate)) - len(pcm)
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

// finish fecha a captura, espera a mixagem drenar e encoda o MP3. Idempotente:
// chamadas repetidas (removeCall + teardown) retornam o mesmo resultado.
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
	if err := cmd.Run(); err != nil {
		return err
	}
	return nil
}

// float32ToLE serializa amostras float32 em little-endian (formato f32le do
// ffmpeg).
func float32ToLE(pcm []float32) []byte {
	buf := make([]byte, len(pcm)*4)
	for i, s := range pcm {
		binary.LittleEndian.PutUint32(buf[i*4:], math.Float32bits(s))
	}
	return buf
}

// Package groupmix mistura (soma) o PCM de vários participantes de uma chamada em
// GRUPO num único stream de playout, e reenquadra em frames de 960 amostras (60ms)
// para o sink. É matemática pura (sem dependência de protocolo/whatsmeow), então é
// testável isoladamente — a parte mais segura do suporte a chamada em grupo.
//
// Portado de github.com/purpshell/meowcaller (MIT, Rajeh Taher): group_audio_mixer.go.
// A derivação de chave/SSRC e a sinalização de grupo (as partes que precisam bater
// byte-a-byte com o servidor do WhatsApp e só validam em chamada real) NÃO estão aqui.
package groupmix

import (
	"slices"
	"sync"
)

const (
	sampleRate   = 16000
	frameSamples = 960 // 60ms @ 16kHz (igual ao MLow)

	chunkSamples    = sampleRate / 100 // 10ms
	prefillSamples  = 2 * frameSamples
	maxQueueSamples = 4 * frameSamples
)

type mixQueue struct {
	pending []float32
	started bool
}

// Mixer soma o PCM por participante. Seguro para uso concorrente.
type Mixer struct {
	mu            sync.Mutex
	streams       map[string]*mixQueue
	allowed       map[string]struct{}
	rosterApplied bool
}

// NewMixer cria um mixer vazio.
func NewMixer() *Mixer {
	return &Mixer{streams: make(map[string]*mixQueue), allowed: make(map[string]struct{})}
}

// ShouldMix diz se vale ativar a mixagem (mais de um participante ativo).
func ShouldMix(activeParticipantIDs []string) bool { return len(activeParticipantIDs) > 1 }

// Add enfileira PCM de um participante. Descarta se o participante não está mais no
// roster aplicado. Devolve false se ignorado.
func (m *Mixer) Add(participantID string, pcm []float32) bool {
	if participantID == "" || len(pcm) == 0 {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.rosterApplied {
		if _, ok := m.allowed[participantID]; !ok {
			return false
		}
	}
	q := m.streams[participantID]
	if q == nil {
		q = &mixQueue{}
		m.streams[participantID] = q
	}
	if len(pcm) >= maxQueueSamples {
		q.pending = append(q.pending[:0], pcm[len(pcm)-maxQueueSamples:]...)
		return true
	}
	if overflow := len(q.pending) + len(pcm) - maxQueueSamples; overflow > 0 {
		q.pending = append([]float32(nil), q.pending[overflow:]...)
	}
	q.pending = append(q.pending, pcm...)
	return true
}

// Retain limita a mixagem aos participantes informados (quem saiu para de contribuir).
func (m *Mixer) Retain(participantIDs []string) {
	allowed := make(map[string]struct{}, len(participantIDs))
	for _, id := range participantIDs {
		if id != "" {
			allowed[id] = struct{}{}
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.allowed = allowed
	m.rosterApplied = true
	for id := range m.streams {
		if _, ok := allowed[id]; !ok {
			delete(m.streams, id)
		}
	}
}

// MixChunk soma um chunk de 10ms de todos os streams prontos, com clamp em [-1,1].
// Devolve ok=false quando ninguém contribuiu.
func (m *Mixer) MixChunk() ([]float32, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()

	ids := make([]string, 0, len(m.streams))
	for id := range m.streams {
		ids = append(ids, id)
	}
	slices.Sort(ids)

	mixed := make([]float32, chunkSamples)
	contributed := false
	for _, id := range ids {
		q := m.streams[id]
		if !q.started {
			if len(q.pending) < prefillSamples {
				continue
			}
			q.started = true
		}
		if len(q.pending) < chunkSamples {
			q.started = false
			continue
		}
		for i := range mixed {
			mixed[i] += q.pending[i]
		}
		q.pending = q.pending[chunkSamples:]
		if len(q.pending) == 0 {
			q.pending = nil
		}
		contributed = true
	}
	if !contributed {
		return nil, false
	}
	for i, s := range mixed {
		if s > 1 {
			mixed[i] = 1
		} else if s < -1 {
			mixed[i] = -1
		}
	}
	return mixed, true
}

// Framer reenquadra os chunks mixados em frames de 960 amostras para o sink.
type Framer struct {
	pending []float32
}

// Push adiciona um chunk e devolve um frame de 960 amostras quando houver o bastante.
func (f *Framer) Push(chunk []float32) ([]float32, bool) {
	f.pending = append(f.pending, chunk...)
	if len(f.pending) < frameSamples {
		return nil, false
	}
	frame := append([]float32(nil), f.pending[:frameSamples]...)
	f.pending = f.pending[frameSamples:]
	if len(f.pending) == 0 {
		f.pending = nil
	}
	return frame, true
}

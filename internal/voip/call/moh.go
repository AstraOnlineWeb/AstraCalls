package call

import "math"

// mohSampleRate é a taxa do áudio de espera — igual à do codec MLow (16 kHz mono).
const mohSampleRate = 16000

// mohLoop é o buffer de música de espera (music on hold), gerado uma única vez no
// carregamento: um motivo curto de notas senoidais suaves, em loop. Serve de fonte
// de áudio para o interlocutor enquanto a chamada está em hold, no lugar do silêncio.
var mohLoop = generateMOH()

// generateMOH sintetiza um arpejo simples e suave (float32 mono a 16 kHz). Amplitude
// baixa e envelope de ataque/decaimento por nota para não estourar nem dar cliques.
func generateMOH() []float32 {
	// Motivo em Hz (0 = pausa). Um arpejo de Lá menor, discreto.
	notes := []float64{440.00, 523.25, 659.25, 523.25, 440.00, 0, 493.88, 587.33, 493.88, 0}
	const (
		noteDur   = 0.34 // segundos por nota
		amplitude = 0.16
	)
	samplesPerNote := int(noteDur * mohSampleRate)
	attack := int(0.012 * mohSampleRate)
	release := int(0.06 * mohSampleRate)

	buf := make([]float32, 0, samplesPerNote*len(notes))
	for _, freq := range notes {
		for i := 0; i < samplesPerNote; i++ {
			var s float64
			if freq > 0 {
				env := 1.0
				if i < attack {
					env = float64(i) / float64(attack)
				} else if i > samplesPerNote-release {
					env = float64(samplesPerNote-i) / float64(release)
				}
				s = amplitude * env * math.Sin(2*math.Pi*freq*float64(i)/mohSampleRate)
			}
			buf = append(buf, float32(s))
		}
	}
	return buf
}

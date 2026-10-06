package media

import "time"

// MediaClock é o relógio de mídia ÚNICO de uma chamada: uma origem de wall-clock a
// partir da qual os timestamps RTP de áudio E vídeo são derivados, começando perto de
// zero e ancorados no instante real da captura.
//
// Por que existe (portado do zapo-js, MIT): o WhatsApp Web alinha o vídeo ao áudio
// subtraindo os dois timestamps RTP e guardando a diferença em MICROSSEGUNDOS num
// int32 COM sinal. Se o áudio e o vídeo começam de pontos aleatórios independentes do
// espaço de 32 bits (o que acontece com timestamps RTP aleatórios), essa diferença
// ESTOURA o int32 e o receptor passa a ler o vídeo como "adiantado" pra sempre: a
// imagem congela, sem log e sem pedir keyframe. Ancorando os dois no mesmo relógio por
// call (perto de zero), a diferença fica em ~0 e o vídeo não congela.
type MediaClock struct {
	origin time.Time
}

// NewMediaClock cria um relógio ancorado agora (início da mídia da call).
func NewMediaClock() *MediaClock {
	return &MediaClock{origin: time.Now()}
}

// Reset reancora a origem no instante atual. Chamado no 1º pacote de áudio (início REAL
// da mídia): o relógio é criado quando a sessão nasce (offer/epoch), mas o áudio só
// começa a fluir no connect; sem reancorar, o vídeo ficaria adiantado do áudio pelo
// tempo de toque e o receptor seguraria/congelaria o vídeo.
func (c *MediaClock) Reset() {
	if c != nil {
		c.origin = time.Now()
	}
}

// TimestampFor devolve o timestamp RTP para uma taxa de amostragem (ex.: 16000 p/ Opus,
// 90000 p/ H264) no instante atual — perto de zero logo após a origem, crescendo em
// tempo real. Serve pra SEMEAR o início de um stream (áudio no começo, vídeo no 1º
// frame), mantendo os dois no mesmo eixo de tempo.
func (c *MediaClock) TimestampFor(rate int) uint32 {
	if c == nil {
		return 0
	}
	return uint32(int64(time.Since(c.origin).Seconds() * float64(rate)))
}

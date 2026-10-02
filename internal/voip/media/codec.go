package media

type Codec interface {
	Encode(pcm []float32) ([]byte, error)

	Decode(frame []byte) ([]float32, error)

	FrameSize() int

	SampleRate() int

	Close()
}

type CodecOptions struct {
	Bitrate    int
	Complexity int
	FEC        bool
}

// Padrões do áudio da chamada (MLow). 6000 era o PISO (pior caso de rede) e
// deixava a voz "rádio AM". O WhatsApp NÃO prende em 6k — como somos a fonte, o
// bitrate que mandamos domina a qualidade que o destino ouve. 16k + complexity 9 +
// FEC melhora muito a voz (referência pure-Go do MLow usa ~20k/8). Vale p/ todas as
// chamadas (ao vivo e broadcast).
var DefaultCodecOptions = CodecOptions{Bitrate: 16000, Complexity: 9, FEC: true}

// VideoCallAudioBitrate é o bitrate de áudio usado em chamada de VÍDEO: áudio e
// vídeo dividem o mesmo canal do relay, então o áudio precisa ser enxuto pra não
// roubar a banda do vídeo (e travar). Em chamada só de áudio usa DefaultCodecOptions.
const VideoCallAudioBitrate = 8000

const (
	mlowSampleRate = 16000
	mlowFrameSize  = 960
)

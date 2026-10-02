package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"time"

	"wacalls/internal/voip/transport"
)

// Broadcast de VÍDEO: injeta um arquivo de vídeo gravado numa chamada de vídeo
// (sem câmera ao vivo). O arquivo é decodificado em frames H264 Annex-B pequenos
// (160x120 @ 15fps, baseline) — o mesmo perfil que o WhatsApp usa em chamada — e
// bombeado via CallManager.FeedCapturedVideo no ritmo real. O áudio do arquivo
// (quando houver) é tocado em paralelo pelo pumpAudio.

const (
	// PADRÕES. Com PACING (pumpVideo espera o canal drenar em vez de dropar) dá pra
	// sustentar bitrate bem acima dos antigos ~50k sem travar — o canal da chamada
	// adapta até ~400k (doc Meta). Cliente ajusta via video_max/video_bitrate/video_fps.
	// Padrão conservador: o caminho gateway→RELAY→telefone costuma sustentar só
	// ~50kbps fluido (o relay do WhatsApp é limitado; o app oficial usa P2P e por isso
	// vai mais alto). Quem tiver rede de gateway melhor sobe via video_bitrate.
	bcVideoMax      = 180
	bcVideoFPS      = 15
	// 48k vídeo + 8k áudio (VideoCallAudioBitrate) = 56k, que é o total que fluía no
	// canal compartilhado do relay. Subir isso trava (áudio+vídeo dividem a banda).
	bcVideoBitrateK = 48
	// watermarks do pacing (bytes bufferizados no canal do relay). Mantemos abaixo do
	// drop do pipeline (48KB): acima de High espera drenar; retoma abaixo de Low.
	bcVideoHighWM = 40 * 1024
	bcVideoLowWM  = 16 * 1024
)

// vframe é um access unit (1 frame) Annex-B + se é keyframe (IDR). O pacing NUNCA
// pula keyframe (dropar IDR congela o vídeo até o próximo); só pula P-frame quando
// o canal está muito congestionado, pra aliviar sem travar.
type vframe struct {
	data []byte
	key  bool
}

// videoOpts controla a qualidade do vídeo do broadcast. Zero = usa o padrão (leve).
type videoOpts struct {
	MaxDim     int // lado maior em px (preserva proporção). Padrão bcVideoMax.
	BitrateK   int // bitrate alvo em kbps. Padrão bcVideoBitrateK.
	FPS        int // quadros/s. Padrão bcVideoFPS.
}

// normalize aplica padrões e limites seguros (evita estourar o canal da chamada).
func (o videoOpts) normalize() videoOpts {
	if o.MaxDim <= 0 {
		o.MaxDim = bcVideoMax
	}
	o.MaxDim = clampInt(o.MaxDim, 120, 480)
	if o.BitrateK <= 0 {
		o.BitrateK = bcVideoBitrateK
	}
	o.BitrateK = clampInt(o.BitrateK, 40, 1200)
	if o.FPS <= 0 {
		o.FPS = bcVideoFPS
	}
	o.FPS = clampInt(o.FPS, 8, 30)
	return o
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// decodeVideoFrames usa ffmpeg para transformar qualquer vídeo em frames H264
// Annex-B no perfil da chamada (baseline), preservando a proporção. Cada elemento
// do slice é UM access unit (1 frame), com SPS/PPS nos keyframes e delimitado por
// AUD. O 1º frame é IDR. opts controla resolução/bitrate/fps (0 = padrão leve).
func decodeVideoFrames(path string, opts videoOpts) ([]vframe, error) {
	o := opts.normalize()
	// preserva a proporção (sem pad): cabe na caixa o.MaxDim x o.MaxDim, lados
	// pares (H264 exige). Vertical sai vertical, horizontal sai horizontal.
	vf := fmt.Sprintf("scale=w=%d:h=%d:force_original_aspect_ratio=decrease:force_divisible_by=2,fps=%d",
		o.MaxDim, o.MaxDim, o.FPS)
	// keyframe a cada ~4s (não a cada 1s): keyframe é um frame GRANDE; mandar um por
	// segundo gera picos que estouram o buffer (48KB) e TRAVAM o vídeo. Menos
	// keyframes = stream mais plano = mais fluido no canal de banda baixa da chamada.
	maxrate := o.BitrateK * 11 / 10 // teto quase colado no alvo (evita estouro)
	g := fmt.Sprintf("%d", o.FPS*4)
	keyMin := fmt.Sprintf("%d", o.FPS)
	br := fmt.Sprintf("%dk", o.BitrateK)

	// Encode em 2-PASSADAS com preset veryslow. Como decodificamos OFFLINE (não é
	// tempo real), podemos gastar CPU pra extrair o MÁXIMO de qualidade por bit — o
	// canal de vídeo da chamada é de banda baixa (buffer dropa > 48KB), então a saída
	// é melhorar a EFICIÊNCIA, não subir bitrate (que congestiona e trava).
	plog, perr := os.CreateTemp("", "wacalls-x264-*")
	passlog := path + ".x264log"
	if perr == nil {
		passlog = plog.Name()
		plog.Close()
	}
	defer func() {
		os.Remove(passlog)
		os.Remove(passlog + "-0.log")
		os.Remove(passlog + "-0.log.mbtree")
	}()

	common := []string{
		"-i", path, "-an", "-vf", vf,
		"-c:v", "libx264", "-profile:v", "baseline", "-pix_fmt", "yuv420p",
		"-preset", "veryslow",
		"-g", g, "-keyint_min", keyMin, "-sc_threshold", "0",
		// bufsize pequeno = rate control mais "duro" = menos picos (frames mais
		// uniformes), o que evita estourar o buffer do canal e travar.
		"-b:v", br, "-maxrate", fmt.Sprintf("%dk", maxrate), "-bufsize", fmt.Sprintf("%dk", o.BitrateK),
		// aq-mode=2 (variance) espalha melhor os bits em bitrate baixo = menos blocagem.
		"-x264-params", "aq-mode=2:aq-strength=1.0",
		"-passlogfile", passlog,
	}
	// passada 1: análise (descarta a saída)
	p1 := append([]string{"-hide_banner", "-loglevel", "error", "-y"}, common...)
	p1 = append(p1, "-pass", "1", "-f", "null", os.DevNull)
	var e1 bytes.Buffer
	c1 := exec.Command("ffmpeg", p1...)
	c1.Stderr = &e1
	if err := c1.Run(); err != nil {
		return nil, fmt.Errorf("ffmpeg vídeo pass1: %v: %s", err, e1.String())
	}
	// passada 2: encode final pra Annex-B
	p2 := append([]string{"-hide_banner", "-loglevel", "error"}, common...)
	p2 = append(p2, "-pass", "2", "-bsf:v", "h264_metadata=aud=insert", "-f", "h264", "pipe:1")
	cmd := exec.Command("ffmpeg", p2...)
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("ffmpeg vídeo pass2: %v: %s", err, errb.String())
	}
	frames := splitH264Frames(out.Bytes())
	if len(frames) == 0 {
		return nil, fmt.Errorf("nenhum frame de vídeo decodificado")
	}
	return frames, nil
}

// splitH264Frames fatia um stream Annex-B em access units (1 por frame), usando o
// AUD (NAL tipo 9) como delimitador de início de frame. Cada frame devolvido é
// Annex-B (NALUs com start code 00000001), pronto p/ FeedCapturedVideo.
func splitH264Frames(annexb []byte) []vframe {
	nalus := transport.SplitAnnexB(annexb)
	var frames []vframe
	var cur []byte
	key := false
	flush := func() {
		if len(cur) > 0 {
			frames = append(frames, vframe{data: cur, key: key})
			cur = nil
			key = false
		}
	}
	for _, n := range nalus {
		if len(n) == 0 {
			continue
		}
		t := n[0] & 0x1f
		if t == 9 { // AUD = começo de um novo access unit
			flush()
		}
		if t == 5 { // NAL tipo 5 = IDR (keyframe)
			key = true
		}
		cur = append(cur, 0, 0, 0, 1)
		cur = append(cur, n...)
	}
	flush()
	return frames
}

// pumpVideo bombeia os frames na chamada no ritmo de fps. Modos:
//   - stop != nil: fica em LOOP (reinicia do keyframe) até stop fechar — usado em
//     paralelo com o áudio (vídeo acompanha a ligação toda).
//   - stop == nil && maxMs > 0: LOOP até maxMs.
//   - stop == nil && maxMs <= 0: toca os frames UMA vez.
// Para também se a chamada cair. Devolve a duração tocada (ms).
func (s *Session) pumpVideo(callID string, frames []vframe, fps int, stop <-chan struct{}, maxMs int) int {
	if fps <= 0 {
		fps = bcVideoFPS
	}
	loop := stop != nil || maxMs > 0
	frameInterval := time.Second / time.Duration(fps)
	start := time.Now()
	next := start
	i := 0
	ended := func() bool {
		if stop != nil {
			select {
			case <-stop:
				return true
			default:
			}
		}
		if maxMs > 0 && time.Since(start) >= time.Duration(maxMs)*time.Millisecond {
			return true
		}
		rec, ok := s.mgr.broker.getCall(callID)
		if !ok || rec == nil || rec.Status == StatusEnded {
			return true
		}
		return false
	}
	for {
		if ended() {
			break
		}
		ac, ok := s.reg.get(callID)
		if !ok {
			break
		}
		if i >= len(frames) {
			if !loop {
				break
			}
			i = 0 // reinicia do keyframe
		}
		f := frames[i]

		// relógio de fps (pace uniforme, sem burst)
		if now := time.Now(); now.Before(next) {
			time.Sleep(next.Sub(now))
		}

		// PACING por congestão: se o canal está cheio, espera drenar (limitado) em vez
		// de deixar o pipeline dropar. Keyframe sempre vai (dropar IDR congela tudo);
		// P-frame é pulado se o canal continuar cheio depois da espera — alivia sem travar.
		buf := ac.cm.VideoBufferedAmount()
		if buf > bcVideoHighWM {
			waited := time.Duration(0)
			for buf > bcVideoLowWM && waited < 400*time.Millisecond {
				if ended() {
					return int(time.Since(start).Milliseconds())
				}
				time.Sleep(10 * time.Millisecond)
				waited += 10 * time.Millisecond
				buf = ac.cm.VideoBufferedAmount()
			}
			if buf > bcVideoHighWM && !f.key {
				i++
				next = next.Add(frameInterval)
				continue // pula este P-frame pra aliviar a congestão
			}
		}

		ac.cm.FeedCapturedVideo(f.data)
		i++
		next = next.Add(frameInterval)
	}
	return int(time.Since(start).Milliseconds())
}

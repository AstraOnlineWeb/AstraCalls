package main

import (
	"bytes"
	"fmt"
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
	// PADRÕES (leves). O lado maior cabe em bcVideoMax preservando a proporção
	// (vertical continua vertical) — sem pad/tarjas. Cliente pode aumentar via
	// video_max/video_bitrate/video_fps no endpoint.
	bcVideoMax      = 180
	bcVideoFPS      = 15
	bcVideoBitrateK = 80
)

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
func decodeVideoFrames(path string, opts videoOpts) ([][]byte, error) {
	o := opts.normalize()
	// preserva a proporção (sem pad): cabe na caixa o.MaxDim x o.MaxDim, lados
	// pares (H264 exige). Vertical sai vertical, horizontal sai horizontal.
	vf := fmt.Sprintf("scale=w=%d:h=%d:force_original_aspect_ratio=decrease:force_divisible_by=2,fps=%d",
		o.MaxDim, o.MaxDim, o.FPS)
	maxrate := o.BitrateK * 4 / 3
	cmd := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error",
		"-i", path, "-an",
		"-vf", vf,
		"-c:v", "libx264", "-profile:v", "baseline", "-pix_fmt", "yuv420p",
		"-g", fmt.Sprintf("%d", o.FPS), "-keyint_min", fmt.Sprintf("%d", o.FPS),
		"-b:v", fmt.Sprintf("%dk", o.BitrateK),
		"-maxrate", fmt.Sprintf("%dk", maxrate),
		"-bufsize", fmt.Sprintf("%dk", o.BitrateK*2),
		"-bsf:v", "h264_metadata=aud=insert",
		"-f", "h264", "pipe:1")
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("ffmpeg vídeo decode: %v: %s", err, errb.String())
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
func splitH264Frames(annexb []byte) [][]byte {
	nalus := transport.SplitAnnexB(annexb)
	var frames [][]byte
	var cur []byte
	flush := func() {
		if len(cur) > 0 {
			frames = append(frames, cur)
			cur = nil
		}
	}
	for _, n := range nalus {
		if len(n) == 0 {
			continue
		}
		if n[0]&0x1f == 9 { // AUD = começo de um novo access unit
			flush()
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
func (s *Session) pumpVideo(callID string, frames [][]byte, fps int, stop <-chan struct{}, maxMs int) int {
	if fps <= 0 {
		fps = bcVideoFPS
	}
	loop := stop != nil || maxMs > 0
	start := time.Now()
	ticker := time.NewTicker(time.Second / time.Duration(fps))
	defer ticker.Stop()
	i := 0
	for {
		<-ticker.C
		if stop != nil {
			select {
			case <-stop:
				return int(time.Since(start).Milliseconds())
			default:
			}
		}
		if maxMs > 0 && time.Since(start) >= time.Duration(maxMs)*time.Millisecond {
			break
		}
		rec, ok := s.mgr.broker.getCall(callID)
		if !ok || rec == nil || rec.Status == StatusEnded {
			break // a outra ponta desligou
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
		ac.cm.FeedCapturedVideo(frames[i])
		i++
	}
	return int(time.Since(start).Milliseconds())
}

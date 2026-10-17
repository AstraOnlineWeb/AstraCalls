package main

import (
	"fmt"
	"strconv"
	"strings"
)

// Codecs e negociação de mídia do SIP: G.711 µ-law (PT 0) e A-law (PT 8),
// DTMF por telephone-event (RFC 4733) e reamostragem 8 kHz <-> 16 kHz com filtro.

const (
	rtpPTPCMU uint8 = 0
	rtpPTPCMA uint8 = 8
)

// ===== G.711 A-law =====

func linearToAlaw(sample int16) byte {
	s := int(sample) >> 3 // A-law trabalha com 13 bits
	mask := 0xD5
	if s < 0 {
		mask = 0x55
		s = -s - 1
	}
	var seg int
	switch {
	case s < 0x20:
		seg = 0
	case s < 0x40:
		seg = 1
	case s < 0x80:
		seg = 2
	case s < 0x100:
		seg = 3
	case s < 0x200:
		seg = 4
	case s < 0x400:
		seg = 5
	case s < 0x800:
		seg = 6
	case s < 0x1000:
		seg = 7
	default:
		return byte(0x7F ^ mask)
	}
	var aval int
	if seg < 2 {
		aval = (s >> 1) & 0x0F
	} else {
		aval = (s >> seg) & 0x0F
	}
	aval |= seg << 4
	return byte(aval ^ mask)
}

func alawToLinear(a byte) int16 {
	a ^= 0x55
	t := int(a&0x0F) << 4
	seg := int(a&0x70) >> 4
	switch seg {
	case 0:
		t += 8
	case 1:
		t += 0x108
	default:
		t += 0x108
		t <<= seg - 1
	}
	if a&0x80 != 0 {
		return int16(t)
	}
	return int16(-t)
}

func g711Decode(pt uint8, b byte) int16 {
	if pt == rtpPTPCMA {
		return alawToLinear(b)
	}
	return ulawToLinear(b)
}

func g711Encode(pt uint8, s int16) byte {
	if pt == rtpPTPCMA {
		return linearToAlaw(s)
	}
	return linearToUlaw(s)
}

func g711Name(pt uint8) string {
	if pt == rtpPTPCMA {
		return "PCMA"
	}
	return "PCMU"
}

// ===== Reamostragem =====

// upsampler8to16 dobra a taxa por interpolação linear (meio-termo entre amostras
// vizinhas), que já atua como passa-baixa — evita o "eco metálico" da duplicação
// pura. Guarda a última amostra para não ter descontinuidade entre pacotes.
type upsampler8to16 struct{ prev float32 }

func (u *upsampler8to16) process(in []float32) []float32 {
	out := make([]float32, 0, len(in)*2)
	for _, s := range in {
		out = append(out, (u.prev+s)/2, s)
		u.prev = s
	}
	return out
}

// downsampler16to8 reduz a taxa à metade com um FIR [1 2 1]/4 antes de decimar
// (corta acima de ~4 kHz e reduz o aliasing que piora o STT do outro lado).
type downsampler16to8 struct {
	prev    float32 // última amostra do bloco anterior
	pending []float32
}

func (d *downsampler16to8) process(in []float32) []float32 {
	buf := append(d.pending, in...)
	n := len(buf) / 2
	out := make([]float32, 0, n)
	for i := 0; i < n; i++ {
		x0 := buf[2*i]
		var xm float32
		if i == 0 {
			xm = d.prev
		} else {
			xm = buf[2*i-1]
		}
		x1 := buf[2*i+1]
		out = append(out, 0.25*xm+0.5*x0+0.25*x1)
	}
	if n > 0 {
		d.prev = buf[2*n-1]
	}
	d.pending = append(d.pending[:0], buf[2*n:]...)
	return out
}

func floatToInt16(s float32) int16 {
	if s > 1 {
		s = 1
	} else if s < -1 {
		s = -1
	}
	return int16(s * 32767)
}

// ===== DTMF (RFC 4733) =====

func dtmfDigit(event byte) string {
	switch {
	case event <= 9:
		return strconv.Itoa(int(event))
	case event == 10:
		return "*"
	case event == 11:
		return "#"
	case event >= 12 && event <= 15:
		return string(rune('A' + event - 12))
	}
	return ""
}

// ===== SDP =====

// sdpOffer é o que interessa da oferta SDP de um INVITE.
type sdpOffer struct {
	addr   string  // IP:porta de RTP do outro lado
	pts    []uint8 // payload types do m=audio, na ordem de preferência
	dtmfPT uint8   // PT do telephone-event/8000 (0 = não ofertado)
}

func parseSDPOffer(sdp string) sdpOffer {
	var o sdpOffer
	var ip, port string
	for _, line := range strings.Split(sdp, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "c=IN IP4 "):
			if f := strings.Fields(line); len(f) >= 3 && ip == "" {
				ip = f[2]
			}
		case strings.HasPrefix(line, "m=audio "):
			f := strings.Fields(line)
			if len(f) >= 2 {
				port = f[1]
			}
			for _, p := range f[3:] {
				if n, err := strconv.Atoi(p); err == nil && n >= 0 && n < 128 {
					o.pts = append(o.pts, uint8(n))
				}
			}
		case strings.HasPrefix(line, "a=rtpmap:"):
			rest := strings.TrimPrefix(line, "a=rtpmap:")
			f := strings.Fields(rest)
			if len(f) == 2 && strings.EqualFold(f[1], "telephone-event/8000") {
				if n, err := strconv.Atoi(f[0]); err == nil && n > 0 && n < 128 {
					o.dtmfPT = uint8(n)
				}
			}
		}
	}
	if ip != "" && port != "" {
		o.addr = ip + ":" + port
	}
	return o
}

// pickG711 escolhe o primeiro codec G.711 da oferta (ok=false se não houver).
func (o sdpOffer) pickG711() (uint8, bool) {
	for _, pt := range o.pts {
		if pt == rtpPTPCMU || pt == rtpPTPCMA {
			return pt, true
		}
	}
	return 0, false
}

// buildSIPSDPAnswer monta o SDP de resposta com um único codec G.711 e,
// se ofertado, o telephone-event no mesmo PT da oferta.
func buildSIPSDPAnswer(localIP string, rtpPort int, pt, dtmfPT uint8) string {
	fmts := strconv.Itoa(int(pt))
	extra := ""
	if dtmfPT != 0 {
		fmts += " " + strconv.Itoa(int(dtmfPT))
		extra = fmt.Sprintf("a=rtpmap:%d telephone-event/8000\na=fmtp:%d 0-16\n", dtmfPT, dtmfPT)
	}
	return fmt.Sprintf("v=0\no=AstraCalls 0 0 IN IP4 %s\ns=AstraCalls SIP Gateway\nc=IN IP4 %s\nt=0 0\nm=audio %d RTP/AVP %s\na=rtpmap:%d %s/8000\n%sa=ptime:20\na=sendrecv\n",
		localIP, localIP, rtpPort, fmts, pt, g711Name(pt), extra)
}

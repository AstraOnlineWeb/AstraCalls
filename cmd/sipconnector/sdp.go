package main

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// SDP mínimo do lado do PABX: só áudio G.711, ptime 20. Sem negociação de outros
// codecs de propósito — G.711 é o que todo PABX fala e é o que o navegador também
// fala nativamente (sem transcodificar).
type sdpAudio struct {
	IP       string
	Port     int
	Payloads []int
}

func parseSDP(body string) (sdpAudio, error) {
	var out sdpAudio
	sessionIP := ""
	inAudio := false
	for _, raw := range strings.Split(body, "\n") {
		line := strings.TrimSpace(raw)
		switch {
		case strings.HasPrefix(line, "c=IN IP4 "):
			ip := strings.Fields(line)[2]
			if inAudio {
				out.IP = ip
			} else {
				sessionIP = ip
			}
		case strings.HasPrefix(line, "m="):
			inAudio = strings.HasPrefix(line, "m=audio ")
			if !inAudio {
				continue
			}
			f := strings.Fields(line)
			if len(f) < 4 {
				return out, errors.New("m=audio inválido")
			}
			out.Port, _ = strconv.Atoi(f[1])
			for _, p := range f[3:] {
				if n, err := strconv.Atoi(p); err == nil {
					out.Payloads = append(out.Payloads, n)
				}
			}
		}
	}
	if out.IP == "" {
		out.IP = sessionIP
	}
	if out.IP == "" || out.Port == 0 {
		return out, errors.New("sdp sem endereço de áudio")
	}
	return out, nil
}

func (a sdpAudio) addr() string {
	return fmt.Sprintf("%s:%d", a.IP, a.Port)
}

// chooseCodec: PCMU primeiro (passa direto para o navegador), depois PCMA.
func chooseCodec(payloads []int) (uint8, bool) {
	for _, want := range []int{int(ptPCMU), int(ptPCMA)} {
		for _, p := range payloads {
			if p == want {
				return uint8(want), true
			}
		}
	}
	return 0, false
}

func buildSDP(ip string, port int, codecs ...uint8) string {
	sid := time.Now().Unix()
	pts := make([]string, len(codecs))
	var maps strings.Builder
	for i, c := range codecs {
		pts[i] = strconv.Itoa(int(c))
		name := "PCMU"
		if c == ptPCMA {
			name = "PCMA"
		}
		fmt.Fprintf(&maps, "a=rtpmap:%d %s/8000\r\n", c, name)
	}
	return fmt.Sprintf("v=0\r\n"+
		"o=sipconnector %d %d IN IP4 %s\r\n"+
		"s=sipconnector\r\n"+
		"c=IN IP4 %s\r\n"+
		"t=0 0\r\n"+
		"m=audio %d RTP/AVP %s\r\n"+
		"%s"+
		"a=ptime:20\r\n"+
		"a=sendrecv\r\n", sid, sid, ip, ip, port, strings.Join(pts, " "), maps.String())
}

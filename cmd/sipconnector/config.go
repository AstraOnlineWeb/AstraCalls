package main

import (
	"net"
	"os"
	"strconv"
	"strings"
	"time"
)

// Configuração do conector SIP. Tudo por variável de ambiente (prefixo SIPC_), no
// mesmo espírito do wacalls (WACALLS_*). O conector é um serviço SEPARADO: portas
// próprias para não colidir com o gateway WhatsApp↔SIP do wacalls (5060).
type config struct {
	HTTPAddr         string        // API HTTP (eventos, chamadas, linhas)
	SIPAddr          string        // escuta SIP (UDP + TCP)
	APIKey           string        // chave-mestra (AstraChat fala com ela)
	PublicIP         string        // IP anunciado no SDP/Contact/ICE
	SIPAdvertisePort int           // porta SIP anunciada no Contact
	RTPMin, RTPMax   int           // faixa de portas RTP do lado do PABX
	WebRTCPort       int           // porta fixa UDP+TCP do WebRTC com o navegador (0 = efêmera)
	DataDir          string        // onde fica o lines.json
	RingTimeout      time.Duration // tempo máximo tocando sem ninguém atender
	TrustAnySource   bool          // aceita INVITE de qualquer IP (só para laboratório)
}

func loadConfig() config {
	c := config{
		HTTPAddr:       envStr("SIPC_HTTP_ADDR", ":8090"),
		SIPAddr:        envStr("SIPC_SIP_ADDR", "0.0.0.0:5070"),
		APIKey:         strings.TrimSpace(os.Getenv("SIPC_API_KEY")),
		RTPMin:         envInt("SIPC_RTP_PORT_MIN", 40100),
		RTPMax:         envInt("SIPC_RTP_PORT_MAX", 40299),
		WebRTCPort:     envInt("SIPC_WEBRTC_PORT", 0),
		DataDir:        envStr("SIPC_DATA_DIR", "./data"),
		RingTimeout:    time.Duration(envInt("SIPC_RING_TIMEOUT_SECONDS", 60)) * time.Second,
		TrustAnySource: envBool("SIPC_TRUST_ANY_SOURCE"),
	}
	c.PublicIP = resolvePublicIP(os.Getenv("SIPC_PUBLIC_IP"))
	_, port, err := net.SplitHostPort(c.SIPAddr)
	defPort := 5070
	if err == nil {
		if n, e := strconv.Atoi(port); e == nil {
			defPort = n
		}
	}
	c.SIPAdvertisePort = envInt("SIPC_SIP_ADVERTISE_PORT", defPort)
	if c.RTPMax < c.RTPMin {
		c.RTPMax = c.RTPMin + 199
	}
	return c
}

// resolvePublicIP: valor fixo quando informado; senão o IPv4 da primeira interface
// não-loopback. Em VPS atrás de NAT o IP público PRECISA ser informado.
func resolvePublicIP(v string) string {
	v = strings.TrimSpace(v)
	if v != "" && !strings.EqualFold(v, "auto") {
		return v
	}
	addrs, err := net.InterfaceAddrs()
	if err == nil {
		for _, a := range addrs {
			if ipn, ok := a.(*net.IPNet); ok && !ipn.IP.IsLoopback() && ipn.IP.To4() != nil {
				return ipn.IP.String()
			}
		}
	}
	return "127.0.0.1"
}

func envStr(k, def string) string {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		return v
	}
	return def
}

func envInt(k string, def int) int {
	if n, err := strconv.Atoi(strings.TrimSpace(os.Getenv(k))); err == nil {
		return n
	}
	return def
}

func envBool(k string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(k))) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

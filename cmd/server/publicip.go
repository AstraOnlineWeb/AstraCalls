package main

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/pion/stun/v3"
)

// =============================================================================
// Descoberta do IP público da VPS (WACALLS_PUBLIC_IP=auto)
// =============================================================================
//
// O IP anunciado ao navegador (ICE NAT 1:1) e no SDP/Contact do SIP precisa ser o
// IP PÚBLICO da máquina. Ler a interface local não basta: dentro do Docker
// (bridge/overlay do Swarm) ela é 10.x/172.x, e em nuvem com NAT (AWS, GCP,
// Oracle, Azure) a VM só enxerga o IP privado.
//
// Ordem de tentativa (para no primeiro IPv4 público válido):
//  1. IP da rota padrão, se já for público (VPS com IP na interface + host network).
//  2. STUN (o mesmo mecanismo do WebRTC): o servidor devolve o IP:porta como o
//     mundo nos vê. Vários servidores em paralelo.
//  3. HTTPS "qual é meu IP" (ipify, AWS checkip, icanhazip, ifconfig.me).
//  4. Último recurso: o IP local, com aviso no log.
//
// O resultado fica em cache no processo. WACALLS_PUBLIC_IP com um IP fixo
// continua tendo prioridade sobre tudo.

var publicIPStunServers = []string{
	"stun.cloudflare.com:3478",
	"stun.l.google.com:19302",
	"stun1.l.google.com:19302",
	"global.stun.twilio.com:3478",
}

var publicIPHTTPServices = []string{
	"https://api.ipify.org",
	"https://checkip.amazonaws.com",
	"https://ipv4.icanhazip.com",
	"https://ifconfig.me/ip",
}

var (
	publicIPMu     sync.Mutex
	publicIPCached string
	publicIPSource string
)

// isPublicIPv4 diz se o IP é IPv4 roteável na internet (não privado, loopback,
// link-local, CGNAT 100.64/10 nem não especificado).
func isPublicIPv4(ip net.IP) bool {
	v4 := ip.To4()
	if v4 == nil || v4.IsUnspecified() || v4.IsLoopback() || v4.IsPrivate() ||
		v4.IsLinkLocalUnicast() || v4.IsMulticast() {
		return false
	}
	if v4[0] == 100 && v4[1]&0xC0 == 64 { // CGNAT 100.64.0.0/10
		return false
	}
	return true
}

// routeLocalIP devolve o IP de saída pela rota padrão (sem enviar pacotes).
func routeLocalIP() net.IP {
	conn, err := net.Dial("udp4", "8.8.8.8:80")
	if err != nil {
		return nil
	}
	defer conn.Close()
	if a, ok := conn.LocalAddr().(*net.UDPAddr); ok {
		return a.IP
	}
	return nil
}

// stunPublicIP faz um Binding Request STUN e devolve o XOR-MAPPED-ADDRESS.
func stunPublicIP(ctx context.Context, server string) (net.IP, error) {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "udp4", server)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}
	req, err := stun.Build(stun.TransactionID, stun.BindingRequest, stun.Fingerprint)
	if err != nil {
		return nil, err
	}
	if _, err := conn.Write(req.Raw); err != nil {
		return nil, err
	}
	buf := make([]byte, 1500)
	for {
		n, err := conn.Read(buf)
		if err != nil {
			return nil, err
		}
		res := &stun.Message{Raw: append([]byte(nil), buf[:n]...)}
		if err := res.Decode(); err != nil || res.TransactionID != req.TransactionID {
			continue
		}
		var xa stun.XORMappedAddress
		if err := xa.GetFrom(res); err == nil {
			return xa.IP, nil
		}
		var ma stun.MappedAddress
		if err := ma.GetFrom(res); err == nil {
			return ma.IP, nil
		}
		return nil, io.ErrUnexpectedEOF
	}
}

// httpPublicIP consulta um serviço HTTPS que responde só o IP em texto.
func httpPublicIP(ctx context.Context, url string) (net.IP, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "AstraCalls")
	resp, err := (&http.Client{Transport: &http.Transport{
		// força IPv4: queremos o IPv4 público (o anunciado no ICE/SDP é IPv4)
		DialContext: func(ctx context.Context, _, addr string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "tcp4", addr)
		},
	}}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, io.ErrUnexpectedEOF
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 64))
	if err != nil {
		return nil, err
	}
	return net.ParseIP(strings.TrimSpace(string(b))), nil
}

// firstPublic roda as sondas em paralelo e devolve o primeiro IPv4 público.
func firstPublic(ctx context.Context, targets []string, probe func(context.Context, string) (net.IP, error)) (string, string) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	type hit struct{ ip, from string }
	ch := make(chan hit, len(targets))
	for _, t := range targets {
		go func(t string) {
			ip, err := probe(ctx, t)
			if err == nil && isPublicIPv4(ip) {
				ch <- hit{ip.To4().String(), t}
				return
			}
			ch <- hit{}
		}(t)
	}
	for range targets {
		if h := <-ch; h.ip != "" {
			return h.ip, h.from
		}
	}
	return "", ""
}

// detectPublicIPWithSource descobre o IP público e diz de onde veio.
func detectPublicIPWithSource(ctx context.Context) (ip, source string) {
	if local := routeLocalIP(); local != nil && isPublicIPv4(local) {
		return local.To4().String(), "interface"
	}
	sctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	ip, from := firstPublic(sctx, publicIPStunServers, stunPublicIP)
	cancel()
	if ip != "" {
		return ip, "stun:" + from
	}
	hctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	ip, from = firstPublic(hctx, publicIPHTTPServices, httpPublicIP)
	cancel()
	if ip != "" {
		return ip, "http:" + from
	}
	if local := routeLocalIP(); local != nil {
		return local.String(), "local (não público)"
	}
	return "", "nenhum"
}

// resolvedPublicIP devolve o IP a anunciar: WACALLS_PUBLIC_IP fixo, ou a detecção
// automática (em cache) quando vale "auto". "" quando a variável não está setada.
func resolvedPublicIP(log *slog.Logger) string {
	env := strings.TrimSpace(os.Getenv("WACALLS_PUBLIC_IP"))
	if env == "" {
		return ""
	}
	if !strings.EqualFold(env, "auto") {
		return env
	}
	publicIPMu.Lock()
	defer publicIPMu.Unlock()
	if publicIPCached != "" {
		return publicIPCached
	}
	ip, src := detectPublicIPWithSource(context.Background())
	if log == nil {
		log = slog.Default()
	}
	if ip == "" {
		log.Warn("WACALLS_PUBLIC_IP=auto: não foi possível descobrir o IP público; defina o IP manualmente")
		return ""
	}
	if strings.HasPrefix(src, "local") {
		log.Warn("WACALLS_PUBLIC_IP=auto: só achei IP local (sem saída para STUN/HTTPS); chamadas podem ficar mudas — defina o IP manualmente", "ip", ip)
	} else {
		log.Info("WACALLS_PUBLIC_IP=auto: IP público detectado", "ip", ip, "fonte", src)
	}
	publicIPCached, publicIPSource = ip, src
	return ip
}

// publicIPInfo devolve o IP e a fonte já resolvidos (para o /api/config).
func publicIPInfo() (string, string) {
	env := strings.TrimSpace(os.Getenv("WACALLS_PUBLIC_IP"))
	if env != "" && !strings.EqualFold(env, "auto") {
		return env, "env"
	}
	publicIPMu.Lock()
	defer publicIPMu.Unlock()
	return publicIPCached, publicIPSource
}

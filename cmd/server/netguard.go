package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	neturl "net/url"
	"time"
)

// Guarda anti-SSRF para downloads de mídia por URL FORNECIDA PELO USUÁRIO
// (endpoints /messages/* que aceitam url). NÃO se aplica ao fetch do data_url do
// Chatwoot (que pode ser um host interno/privado legítimo).
//
// A validação é feita no DialContext: resolve o host e recusa se QUALQUER IP for
// loopback, privado (RFC1918/ULA), link-local (inclui 169.254.169.254 =
// metadata de cloud), CGNAT (100.64/10), multicast ou unspecified. Como o dial
// é revalidado a cada conexão (inclusive em cada redirect), fecha DNS rebinding
// e redirect-para-interno.

var errBlockedAddr = errors.New("endereço bloqueado (rede interna/privada)")

func isBlockedIP(ip net.IP) bool {
	if ip == nil {
		return true
	}
	if ip.IsLoopback() || ip.IsUnspecified() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() ||
		ip.IsPrivate() { // 10/8, 172.16/12, 192.168/16, fc00::/7
		return true
	}
	if ip4 := ip.To4(); ip4 != nil {
		// CGNAT 100.64.0.0/10
		if ip4[0] == 100 && ip4[1] >= 64 && ip4[1] <= 127 {
			return true
		}
	}
	return false
}

func ssrfSafeDial(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	var target net.IP
	for _, ipa := range ips {
		if isBlockedIP(ipa.IP) {
			return nil, fmt.Errorf("%w: %s", errBlockedAddr, ipa.IP)
		}
		if target == nil {
			target = ipa.IP
		}
	}
	if target == nil {
		return nil, errBlockedAddr
	}
	d := &net.Dialer{Timeout: 10 * time.Second}
	return d.DialContext(ctx, network, net.JoinHostPort(target.String(), port))
}

// cliente HTTP com a guarda SSRF, para downloads de URL do usuário.
var guardedMediaHTTP = &http.Client{
	Timeout:   30 * time.Second,
	Transport: &http.Transport{DialContext: ssrfSafeDial},
}

// validateUserURL recusa esquemas que não sejam http/https (bloqueia file://,
// gopher://, etc.). A checagem de IP é feita no dial.
func validateUserURL(raw string) error {
	u, err := neturl.Parse(raw)
	if err != nil {
		return err
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return errors.New("apenas http/https são permitidos")
	}
	if u.Hostname() == "" {
		return errors.New("url inválida")
	}
	return nil
}

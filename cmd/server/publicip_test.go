package main

import (
	"context"
	"net"
	"os"
	"testing"
	"time"

	"github.com/pion/stun/v3"
)

func TestIsPublicIPv4(t *testing.T) {
	cases := map[string]bool{
		"91.107.209.253": true, "8.8.8.8": true,
		"10.0.0.5": false, "172.18.0.3": false, "192.168.1.10": false,
		"127.0.0.1": false, "169.254.1.1": false, "100.64.0.1": false, "100.127.255.254": false,
		"100.128.0.1": true, "0.0.0.0": false, "2001:db8::1": false,
	}
	for s, want := range cases {
		if got := isPublicIPv4(net.ParseIP(s)); got != want {
			t.Errorf("isPublicIPv4(%s) = %v; quer %v", s, got, want)
		}
	}
}

// Servidor STUN falso local: responde o XOR-MAPPED-ADDRESS 203.0.113.7.
func TestStunPublicIPParsesMappedAddress(t *testing.T) {
	pc, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	go func() {
		buf := make([]byte, 1500)
		n, addr, err := pc.ReadFrom(buf)
		if err != nil {
			return
		}
		req := &stun.Message{Raw: buf[:n]}
		if req.Decode() != nil {
			return
		}
		res, _ := stun.Build(stun.NewTransactionIDSetter(req.TransactionID), stun.BindingSuccess,
			&stun.XORMappedAddress{IP: net.ParseIP("203.0.113.7"), Port: 4444}, stun.Fingerprint)
		_, _ = pc.WriteTo(res.Raw, addr)
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	ip, err := stunPublicIP(ctx, pc.LocalAddr().String())
	if err != nil || ip.String() != "203.0.113.7" {
		t.Fatalf("stunPublicIP = %v, %v", ip, err)
	}
}

func TestResolvedPublicIPFixedEnv(t *testing.T) {
	t.Setenv("WACALLS_PUBLIC_IP", "198.51.100.9")
	if got := resolvedPublicIP(nil); got != "198.51.100.9" {
		t.Fatalf("IP fixo ignorado: %q", got)
	}
	t.Setenv("WACALLS_PUBLIC_IP", "")
	if got := resolvedPublicIP(nil); got != "" {
		t.Fatalf("sem variável deveria ser vazio: %q", got)
	}
}

// Detecção real (rede): WACALLS_NET_TEST=1 go test -run TestDetectPublicIPLive -v
func TestDetectPublicIPLive(t *testing.T) {
	if os.Getenv("WACALLS_NET_TEST") == "" {
		t.Skip("teste de rede: defina WACALLS_NET_TEST=1")
	}
	ip, src := detectPublicIPWithSource(context.Background())
	t.Logf("detectado ip=%s fonte=%s", ip, src)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	for _, s := range publicIPStunServers {
		sip, err := stunPublicIP(ctx, s)
		t.Logf("stun %s → %v %v", s, sip, err)
	}
	if !isPublicIPv4(net.ParseIP(ip)) {
		t.Fatalf("não achou IP público: %s (%s)", ip, src)
	}
}

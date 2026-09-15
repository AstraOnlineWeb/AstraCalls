package main

import (
	"net"
	"testing"
)

func TestIsBlockedIP(t *testing.T) {
	blocked := []string{"127.0.0.1", "10.1.2.3", "192.168.0.1", "172.16.5.5", "169.254.169.254", "100.64.0.1", "0.0.0.0", "::1", "fe80::1", "fc00::1"}
	allowed := []string{"8.8.8.8", "1.1.1.1", "46.202.149.139", "2606:4700:4700::1111"}
	for _, s := range blocked {
		if !isBlockedIP(net.ParseIP(s)) {
			t.Errorf("%s deveria ser BLOQUEADO", s)
		}
	}
	for _, s := range allowed {
		if isBlockedIP(net.ParseIP(s)) {
			t.Errorf("%s deveria ser PERMITIDO", s)
		}
	}
}

func TestValidateUserURL(t *testing.T) {
	for _, ok := range []string{"http://example.com/x.jpg", "https://cdn.site.com/a"} {
		if err := validateUserURL(ok); err != nil {
			t.Errorf("%s deveria passar: %v", ok, err)
		}
	}
	for _, bad := range []string{"file:///etc/passwd", "gopher://x", "ftp://x/y", "notaurl"} {
		if err := validateUserURL(bad); err == nil {
			t.Errorf("%s deveria falhar", bad)
		}
	}
}

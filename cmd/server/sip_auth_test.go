package main

import "testing"

// buildAuth monta um header Authorization Digest válido para os parâmetros dados.
func buildAuth(username, realm, password, method, uri, nonce string) string {
	ha1 := md5hex(username + ":" + realm + ":" + password)
	ha2 := md5hex(method + ":" + uri)
	resp := md5hex(ha1 + ":" + nonce + ":" + ha2)
	return `Digest username="` + username + `", realm="` + realm +
		`", nonce="` + nonce + `", uri="` + uri + `", response="` + resp + `"`
}

func TestValidateDigestAuth(t *testing.T) {
	const (
		user   = "wa_ab12cd34"
		realm  = "astracalls"
		pass   = "s3cr3tp4ss"
		method = "REGISTER"
		uri    = "sip:astracalls"
		nonce  = "deadbeef"
	)

	good := buildAuth(user, realm, pass, method, uri, nonce)
	if !validateDigestAuth(good, method, pass) {
		t.Fatal("senha correta deveria validar")
	}
	if validateDigestAuth(good, method, "senha-errada") {
		t.Fatal("senha errada NÃO deveria validar")
	}
	if validateDigestAuth(buildAuth(user, realm, pass, method, uri, nonce), "INVITE", pass) {
		t.Fatal("método divergente NÃO deveria validar")
	}
	if validateDigestAuth("Digest username=\"x\"", method, pass) {
		t.Fatal("header incompleto NÃO deveria validar")
	}
}

func TestValidateDigestAuthQop(t *testing.T) {
	const (
		user, realm, pass = "wa_x", "astracalls", "pw"
		method, uri       = "REGISTER", "sip:astracalls"
		nonce, nc, cnonce = "abc123", "00000001", "0a4f113b"
	)
	ha1 := md5hex(user + ":" + realm + ":" + pass)
	ha2 := md5hex(method + ":" + uri)
	resp := md5hex(ha1 + ":" + nonce + ":" + nc + ":" + cnonce + ":auth:" + ha2)
	hdr := `Digest username="` + user + `", realm="` + realm + `", nonce="` + nonce +
		`", uri="` + uri + `", qop=auth, nc=` + nc + `, cnonce="` + cnonce + `", response="` + resp + `"`
	if !validateDigestAuth(hdr, method, pass) {
		t.Fatal("digest com qop=auth deveria validar")
	}
}

func TestSipUserPart(t *testing.T) {
	cases := map[string]string{
		"5511999999999@s.whatsapp.net":      "5511999999999",
		"5511999999999:12@s.whatsapp.net":   "5511999999999",
		"5511999999999.0:3@s.whatsapp.net":  "5511999999999",
		"5511999999999":                     "5511999999999",
	}
	for in, want := range cases {
		if got := sipUserPart(in); got != want {
			t.Fatalf("sipUserPart(%q) = %q, quero %q", in, got, want)
		}
	}
}

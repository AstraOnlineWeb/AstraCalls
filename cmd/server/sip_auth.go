package main

import (
	"crypto/md5"
	"crypto/subtle"
	"encoding/hex"
	"strings"
)

// Autenticação SIP Digest (RFC 2617, MD5) para o REGISTER.

func md5hex(s string) string {
	sum := md5.Sum([]byte(s))
	return hex.EncodeToString(sum[:])
}

// splitDigestParams divide a lista de parâmetros do header respeitando aspas.
func splitDigestParams(s string) []string {
	var parts []string
	var b strings.Builder
	inQuote := false
	for _, r := range s {
		switch r {
		case '"':
			inQuote = !inQuote
			b.WriteRune(r)
		case ',':
			if inQuote {
				b.WriteRune(r)
			} else {
				parts = append(parts, b.String())
				b.Reset()
			}
		default:
			b.WriteRune(r)
		}
	}
	if strings.TrimSpace(b.String()) != "" {
		parts = append(parts, b.String())
	}
	return parts
}

// parseDigestHeader transforma o valor do header Authorization num mapa de chaves.
func parseDigestHeader(v string) map[string]string {
	out := map[string]string{}
	v = strings.TrimSpace(v)
	if i := strings.IndexByte(v, ' '); i >= 0 && strings.EqualFold(v[:i], "Digest") {
		v = v[i+1:]
	}
	for _, part := range splitDigestParams(v) {
		eq := strings.IndexByte(part, '=')
		if eq < 0 {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(part[:eq]))
		val := strings.Trim(strings.TrimSpace(part[eq+1:]), `"`)
		out[key] = val
	}
	return out
}

// validateDigestAuth confere a resposta Digest do cliente contra a senha da sessão.
func validateDigestAuth(authValue, method, password string) bool {
	p := parseDigestHeader(authValue)
	username := p["username"]
	realm := p["realm"]
	nonce := p["nonce"]
	uri := p["uri"]
	response := strings.ToLower(p["response"])
	qop := p["qop"]
	if username == "" || nonce == "" || uri == "" || response == "" {
		return false
	}

	ha1 := md5hex(username + ":" + realm + ":" + password)
	ha2 := md5hex(method + ":" + uri)

	var expected string
	if qop == "auth" || qop == "auth-int" {
		expected = md5hex(ha1 + ":" + nonce + ":" + p["nc"] + ":" + p["cnonce"] + ":auth:" + ha2)
	} else {
		expected = md5hex(ha1 + ":" + nonce + ":" + ha2)
	}
	return subtle.ConstantTimeCompare([]byte(expected), []byte(response)) == 1
}

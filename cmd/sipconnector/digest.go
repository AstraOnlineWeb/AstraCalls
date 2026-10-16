package main

import (
	"crypto/md5"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"strings"
	"sync"
	"time"
)

// Digest MD5 (RFC 2617) para o PABX que registra no conector (modo register_in).
// Diferente do wacalls, o nonce é GUARDADO e validado: um Authorization capturado
// não serve para registrar de novo depois que o nonce vence.
const (
	digestRealm   = "sipconnector"
	nonceLifetime = 5 * time.Minute
)

type nonceStore struct {
	mu     sync.Mutex
	nonces map[string]time.Time
}

func newNonceStore() *nonceStore {
	return &nonceStore{nonces: map[string]time.Time{}}
}

func (n *nonceStore) issue() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	nonce := hex.EncodeToString(b)
	n.mu.Lock()
	defer n.mu.Unlock()
	now := time.Now()
	for k, exp := range n.nonces {
		if now.After(exp) {
			delete(n.nonces, k)
		}
	}
	n.nonces[nonce] = now.Add(nonceLifetime)
	return nonce
}

func (n *nonceStore) valid(nonce string) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	exp, ok := n.nonces[nonce]
	return ok && time.Now().Before(exp)
}

func md5hex(s string) string {
	sum := md5.Sum([]byte(s))
	return hex.EncodeToString(sum[:])
}

// splitDigestParams divide os parâmetros respeitando aspas.
func splitDigestParams(s string) []string {
	var parts []string
	var b strings.Builder
	inQuote := false
	for _, r := range s {
		switch {
		case r == '"':
			inQuote = !inQuote
			b.WriteRune(r)
		case r == ',' && !inQuote:
			parts = append(parts, b.String())
			b.Reset()
		default:
			b.WriteRune(r)
		}
	}
	if strings.TrimSpace(b.String()) != "" {
		parts = append(parts, b.String())
	}
	return parts
}

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
		out[strings.ToLower(strings.TrimSpace(part[:eq]))] = strings.Trim(strings.TrimSpace(part[eq+1:]), `"`)
	}
	return out
}

// checkDigest valida usuário, nonce emitido por nós e a resposta MD5.
func (n *nonceStore) checkDigest(authValue, method, user, password string) bool {
	p := parseDigestHeader(authValue)
	if p["username"] != user || p["uri"] == "" || p["response"] == "" || !n.valid(p["nonce"]) {
		return false
	}
	ha1 := md5hex(p["username"] + ":" + p["realm"] + ":" + password)
	ha2 := md5hex(method + ":" + p["uri"])
	var expected string
	if qop := p["qop"]; qop == "auth" || qop == "auth-int" {
		expected = md5hex(ha1 + ":" + p["nonce"] + ":" + p["nc"] + ":" + p["cnonce"] + ":auth:" + ha2)
	} else {
		expected = md5hex(ha1 + ":" + p["nonce"] + ":" + ha2)
	}
	return subtle.ConstantTimeCompare([]byte(expected), []byte(strings.ToLower(p["response"]))) == 1
}

func (n *nonceStore) challenge() string {
	return `Digest realm="` + digestRealm + `", nonce="` + n.issue() + `", algorithm=MD5, qop="auth"`
}

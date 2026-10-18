package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Duas credenciais, como no wacalls:
//   - chave-mestra (SIPC_API_KEY): o AstraChat (servidor) e a ponte de voz;
//   - token de widget efêmero, escopado a UMA conta: vai para o navegador do
//     atendente. Só enxerga eventos e chamadas da própria conta.
type principal struct {
	admin     bool
	accountID int // 0 quando admin
}

const (
	widgetTokenPrefix     = "sct1"
	widgetTokenDefaultTTL = 3600
	widgetTokenMaxTTL     = 86400
)

type widgetClaims struct {
	AccountID int   `json:"acc"`
	Exp       int64 `json:"exp"`
}

func (s *server) signingSecret() []byte {
	mac := hmac.New(sha256.New, []byte(s.cfg.APIKey))
	mac.Write([]byte("sipconnector-widget-token-v1"))
	return mac.Sum(nil)
}

func (s *server) signWidgetToken(c widgetClaims) string {
	payload, _ := json.Marshal(c)
	body := widgetTokenPrefix + "." + base64.RawURLEncoding.EncodeToString(payload)
	mac := hmac.New(sha256.New, s.signingSecret())
	mac.Write([]byte(body))
	return body + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (s *server) parseWidgetToken(tok string) (widgetClaims, bool) {
	var c widgetClaims
	parts := strings.Split(tok, ".")
	if len(parts) != 3 || parts[0] != widgetTokenPrefix {
		return c, false
	}
	mac := hmac.New(sha256.New, s.signingSecret())
	mac.Write([]byte(parts[0] + "." + parts[1]))
	if !hmac.Equal([]byte(base64.RawURLEncoding.EncodeToString(mac.Sum(nil))), []byte(parts[2])) {
		return c, false
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || json.Unmarshal(payload, &c) != nil {
		return c, false
	}
	if c.AccountID <= 0 || c.Exp <= time.Now().Unix() {
		return c, false
	}
	return c, true
}

func requestKey(r *http.Request) string {
	if k := r.Header.Get("X-API-Key"); k != "" {
		return k
	}
	if a := r.Header.Get("Authorization"); strings.HasPrefix(strings.ToLower(a), "bearer ") {
		return strings.TrimSpace(a[7:])
	}
	return r.URL.Query().Get("apiKey")
}

func (s *server) authenticate(r *http.Request) (principal, bool) {
	key := requestKey(r)
	if key == "" {
		return principal{}, false
	}
	if subtle.ConstantTimeCompare([]byte(key), []byte(s.cfg.APIKey)) == 1 {
		return principal{admin: true}, true
	}
	if c, ok := s.parseWidgetToken(key); ok {
		return principal{accountID: c.AccountID}, true
	}
	return principal{}, false
}

// canSee: admin vê tudo; token de widget só a própria conta.
func (p principal) canSee(accountID int) bool {
	return p.admin || p.accountID == accountID
}

type authedHandler func(w http.ResponseWriter, r *http.Request, p principal)

func (s *server) requireAuth(h authedHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, ok := s.authenticate(r)
		if !ok {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}
		h(w, r, p)
	}
}

func (s *server) requireAdmin(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, ok := s.authenticate(r)
		if !ok || !p.admin {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}
		h(w, r)
	}
}

// POST /api/widget-tokens {accountId, ttlSeconds?} — só com a chave-mestra. Mesmo
// formato de resposta do wacalls (Kanban::AstracallWidgetToken lê token/expiresAt).
func (s *server) handleWidgetToken(w http.ResponseWriter, r *http.Request) {
	var body struct {
		AccountID  int `json:"accountId"`
		TTLSeconds int `json:"ttlSeconds"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if body.AccountID <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "accountId é obrigatório (> 0)"})
		return
	}
	ttl := body.TTLSeconds
	if ttl <= 0 {
		ttl = widgetTokenDefaultTTL
	}
	ttl = min(ttl, widgetTokenMaxTTL)
	exp := time.Now().Add(time.Duration(ttl) * time.Second)
	writeJSON(w, http.StatusOK, map[string]any{
		"token":     s.signWidgetToken(widgetClaims{AccountID: body.AccountID, Exp: exp.Unix()}),
		"accountId": body.AccountID,
		"expiresAt": exp.UTC().Format(time.RFC3339),
		"scope":     "account",
	})
}

// accountFromClientID: "astrachat_accN" -> N (mesma convenção do wacalls).
func accountFromClientID(cid string) int {
	const prefix = "astrachat_acc"
	if !strings.HasPrefix(cid, prefix) {
		return 0
	}
	n, _ := strconv.Atoi(strings.TrimPrefix(cid, prefix))
	return n
}

func clientID(r *http.Request) string {
	if id := r.Header.Get("X-Client-Id"); id != "" {
		return id
	}
	return r.URL.Query().Get("clientId")
}

func withCORS(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-Client-Id, X-API-Key, Authorization")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		h.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

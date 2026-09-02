package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"time"
)

// Token de widget EFÊMERO e por conta (pedido do AstraChat): quem tem a chave-mestra
// pede um token escopado a uma conta (e opcionalmente uma inbox), com expiração. O
// token abre SÓ a superfície de widget (eventos SSE + operações de chamada + resolve),
// e no SSE só recebe eventos da PRÓPRIA conta. Assim a chave-mestra nunca vai pro
// navegador; se o token vazar, o estrago fica limitado à conta e ao tempo de vida.
//
// O token é auto-contido (HMAC-assinado), então não precisa de storage. A chave de
// assinatura vem de WACALLS_WIDGET_SIGNING_SECRET; se não setada, é derivada da
// chave-mestra (WACALLS_API_KEY) — funciona sem config extra e rotacionar a mestra
// invalida todos os tokens.

const (
	widgetTokenPrefix     = "wt1"
	widgetTokenDefaultTTL = 3600  // 1h
	widgetTokenMaxTTL     = 86400 // 24h
)

func widgetSigningSecret() []byte {
	if s := strings.TrimSpace(os.Getenv("WACALLS_WIDGET_SIGNING_SECRET")); s != "" {
		return []byte(s)
	}
	mac := hmac.New(sha256.New, []byte(os.Getenv("WACALLS_API_KEY")))
	mac.Write([]byte("wacalls-widget-token-v1"))
	return mac.Sum(nil)
}

type widgetTokenClaims struct {
	AccountID int   `json:"acc"`
	InboxID   int   `json:"inbox,omitempty"`
	Exp       int64 `json:"exp"` // unix seconds
}

func signWidgetToken(c widgetTokenClaims) string {
	payload, _ := json.Marshal(c)
	p := base64.RawURLEncoding.EncodeToString(payload)
	body := widgetTokenPrefix + "." + p
	mac := hmac.New(sha256.New, widgetSigningSecret())
	mac.Write([]byte(body))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return body + "." + sig
}

// parseWidgetToken valida prefixo, assinatura e expiração. Só devolve ok=true para
// um token íntegro e ainda válido.
func parseWidgetToken(tok string) (widgetTokenClaims, bool) {
	var c widgetTokenClaims
	parts := strings.Split(tok, ".")
	if len(parts) != 3 || parts[0] != widgetTokenPrefix {
		return c, false
	}
	mac := hmac.New(sha256.New, widgetSigningSecret())
	mac.Write([]byte(parts[0] + "." + parts[1]))
	expected := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(expected), []byte(parts[2])) {
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

// widgetTokenAccountMatches confere que a conta pedida na requisição (accountId da
// query, ou derivada do clientId astrachat_accN) bate com a conta do token.
func widgetTokenAccountMatches(r *http.Request, c widgetTokenClaims) bool {
	acc := asInt(r.URL.Query().Get("accountId"))
	if acc == 0 {
		acc = accountFromClientID(clientID(r))
	}
	return acc != 0 && acc == c.AccountID
}

// GET /api/widget-key — só com a chave-mestra. Devolve a chave de widget ESTÁTICA
// (por instância) se WACALLS_WIDGET_KEY estiver configurada; senão responde 409
// explícito apontando para o fluxo recomendado (tokens por conta).
func (s *server) handleWidgetKey(w http.ResponseWriter, r *http.Request) {
	wk := strings.TrimSpace(os.Getenv("WACALLS_WIDGET_KEY"))
	if wk == "" {
		writeJSON(w, http.StatusConflict, map[string]string{
			"error":   "widget_key_not_configured",
			"message": "WACALLS_WIDGET_KEY não está definida neste servidor. Use POST /api/widget-tokens (com a chave-mestra) para obter um token de widget efêmero e escopado por conta — recomendado.",
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"widgetKey": wk,
		"scope":     "instance", // a chave estática vale para a instância inteira
	})
}

// POST /api/widget-tokens — só com a chave-mestra. Body: {accountId, inboxId?, ttlSeconds?}.
// Devolve um token de widget efêmero escopado à conta.
func (s *server) handleWidgetToken(w http.ResponseWriter, r *http.Request) {
	var body struct {
		AccountID  int `json:"accountId"`
		InboxID    int `json:"inboxId"`
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
	if ttl > widgetTokenMaxTTL {
		ttl = widgetTokenMaxTTL
	}
	exp := time.Now().Add(time.Duration(ttl) * time.Second)
	tok := signWidgetToken(widgetTokenClaims{AccountID: body.AccountID, InboxID: body.InboxID, Exp: exp.Unix()})
	writeJSON(w, http.StatusOK, map[string]any{
		"token":     tok,
		"accountId": body.AccountID,
		"inboxId":   body.InboxID,
		"expiresAt": exp.UTC().Format(time.RFC3339),
		"scope":     "account",
	})
}

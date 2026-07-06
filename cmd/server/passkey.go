package main

import (
	"encoding/json"
	"io"
	"net/http"

	"go.mau.fi/whatsmeow/types"
)

// POST /api/sessions/{sid}/pair-passkey
// Body: a assinatura WebAuthn (resultado de navigator.credentials.get) devolvida
// pelo autenticador do dono, no formato types.WebAuthnResponse. Aceita tanto o
// objeto direto quanto {"assertion": {...}}.
//
// Fluxo: durante o pareamento de uma conta com passkey, o whatsmeow emite o
// desafio via estado de auth (state="passkey_request", campo "passkey"). O front
// roda a assinatura na origem web.whatsapp.com (extensão) e envia o resultado
// aqui; nós repassamos ao WhatsApp com SendPasskeyResponse. O sucesso chega
// depois via events.Connected (state="open").
func (s *server) handlePairPasskey(w http.ResponseWriter, r *http.Request) {
	// sessão existe, mas AINDA NÃO está pareada (estamos no meio do pareamento)
	sess := s.sessionByID(w, r.PathValue("sid"))
	if sess == nil {
		return
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20)) // assinatura é pequena; teto 1MB
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid body"})
		return
	}
	// desembrulha {"assertion": {...}} se vier assim
	var wrap struct {
		Assertion json.RawMessage `json:"assertion"`
	}
	if json.Unmarshal(raw, &wrap) == nil && len(wrap.Assertion) > 0 {
		raw = wrap.Assertion
	}
	var resp types.WebAuthnResponse
	if err := json.Unmarshal(raw, &resp); err != nil || resp.ID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid WebAuthn assertion"})
		return
	}
	if err := sess.client.SendPasskeyResponse(r.Context(), &resp); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

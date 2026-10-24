package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path"
	"strings"
	"time"
)

// Chaves MCP por sessão: o dono da instância gera, no painel ou pela API, uma
// chave presa a UM número e entrega só ela (ou a URL pronta) a quem vai ligar o
// agente de IA. Com ela o agente enxerga e usa apenas essa sessão, sem acesso a
// apagar/parear/configurar a conta. A chave é mostrada uma única vez; no banco
// fica só o SHA-256.

const (
	mcpKeyPrefix     = "acmcp_"
	mcpKeysPerSess   = 20
	mcpKeyNameMaxLen = 60
)

type mcpKey struct {
	ID         string     `json:"id"`
	SessionID  string     `json:"sessionId"`
	Name       string     `json:"name"`
	Prefix     string     `json:"prefix"`
	ReadOnly   bool       `json:"readOnly"`
	CreatedAt  time.Time  `json:"createdAt"`
	LastUsedAt *time.Time `json:"lastUsedAt,omitempty"`
}

// mcpScope é o alcance de uma conexão MCP: zero = chave-mestra (tudo).
type mcpScope struct {
	SessionID string
	ReadOnly  bool
}

func (sc mcpScope) scoped() bool { return sc.SessionID != "" }

func newMCPKey() (key, hash string) {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	key = mcpKeyPrefix + hex.EncodeToString(b)
	return key, mcpKeyHash(key)
}

func mcpKeyHash(key string) string {
	h := sha256.Sum256([]byte(key))
	return hex.EncodeToString(h[:])
}

// ---------- banco ----------

func (st *sessionStore) createMCPKey(ctx context.Context, sid, name string, readOnly bool) (mcpKey, string, error) {
	var n int
	if err := st.db.QueryRowContext(ctx, `SELECT count(*) FROM mcp_keys WHERE session_id = $1`, sid).Scan(&n); err != nil {
		return mcpKey{}, "", err
	}
	if n >= mcpKeysPerSess {
		return mcpKey{}, "", errMCPKeyLimit
	}
	key, hash := newMCPKey()
	k := mcpKey{ID: genSIPCredential(8), SessionID: sid, Name: name, Prefix: key[:len(mcpKeyPrefix)+6], ReadOnly: readOnly}
	err := st.db.QueryRowContext(ctx, `INSERT INTO mcp_keys (id, session_id, name, key_hash, prefix, read_only)
		VALUES ($1, $2, $3, $4, $5, $6) RETURNING created_at`,
		k.ID, sid, name, hash, k.Prefix, readOnly).Scan(&k.CreatedAt)
	return k, key, err
}

var errMCPKeyLimit = errors.New("limite de chaves MCP por sessão atingido")

func (st *sessionStore) listMCPKeys(ctx context.Context, sid string) ([]mcpKey, error) {
	rows, err := st.db.QueryContext(ctx, `SELECT id, session_id, name, prefix, read_only, created_at, last_used_at
		FROM mcp_keys WHERE session_id = $1 ORDER BY created_at`, sid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []mcpKey{}
	for rows.Next() {
		var k mcpKey
		var last sql.NullTime
		if err := rows.Scan(&k.ID, &k.SessionID, &k.Name, &k.Prefix, &k.ReadOnly, &k.CreatedAt, &last); err != nil {
			return nil, err
		}
		if last.Valid {
			k.LastUsedAt = &last.Time
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

func (st *sessionStore) deleteMCPKey(ctx context.Context, sid, id string) (bool, error) {
	res, err := st.db.ExecContext(ctx, `DELETE FROM mcp_keys WHERE session_id = $1 AND id = $2`, sid, id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// lookupMCPKey acha a chave pelo hash e marca o último uso (no máximo 1x/min).
func (st *sessionStore) lookupMCPKey(ctx context.Context, key string) (mcpKey, bool) {
	var k mcpKey
	err := st.db.QueryRowContext(ctx, `SELECT id, session_id, read_only FROM mcp_keys WHERE key_hash = $1`,
		mcpKeyHash(key)).Scan(&k.ID, &k.SessionID, &k.ReadOnly)
	if err != nil {
		return mcpKey{}, false
	}
	go func(id string) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = st.db.ExecContext(ctx, `UPDATE mcp_keys SET last_used_at = now()
			WHERE id = $1 AND (last_used_at IS NULL OR last_used_at < now() - interval '1 minute')`, id)
	}(k.ID)
	return k, true
}

// ---------- rotas de gerenciamento (chave-mestra) ----------

func mcpEndpoints(r *http.Request) (endpoint string) {
	return publicBaseURL(r) + "/mcp"
}

// GET /api/sessions/{sid}/mcp-keys
func (s *server) handleListMCPKeys(w http.ResponseWriter, r *http.Request) {
	sess := s.sessionByID(w, r.PathValue("sid"))
	if sess == nil {
		return
	}
	keys, err := s.sessions.store.listMCPKeys(r.Context(), sess.id)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"keys": keys, "max": mcpKeysPerSess, "endpoint": mcpEndpoints(r)})
}

// POST /api/sessions/{sid}/mcp-keys {name, readOnly}
func (s *server) handleCreateMCPKey(w http.ResponseWriter, r *http.Request) {
	sess := s.sessionByID(w, r.PathValue("sid"))
	if sess == nil {
		return
	}
	var body struct {
		Name     string `json:"name"`
		ReadOnly bool   `json:"readOnly"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	name := strings.TrimSpace(body.Name)
	if len([]rune(name)) > mcpKeyNameMaxLen {
		name = string([]rune(name)[:mcpKeyNameMaxLen])
	}
	k, key, err := s.sessions.store.createMCPKey(r.Context(), sess.id, name, body.ReadOnly)
	if errors.Is(err, errMCPKeyLimit) {
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	ep := mcpEndpoints(r)
	writeJSON(w, http.StatusCreated, map[string]any{
		"key":      k,
		"secret":   key,            // mostrado só agora
		"endpoint": ep,             // usar com Authorization: Bearer <secret>
		"url":      ep + "/" + key, // URL completa (clientes que só aceitam URL)
	})
}

// DELETE /api/sessions/{sid}/mcp-keys/{kid}
func (s *server) handleDeleteMCPKey(w http.ResponseWriter, r *http.Request) {
	sess := s.sessionByID(w, r.PathValue("sid"))
	if sess == nil {
		return
	}
	ok, err := s.sessions.store.deleteMCPKey(r.Context(), sess.id, r.PathValue("kid"))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "chave não encontrada"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"deleted": true})
}

// ---------- endpoint MCP com chave de sessão ----------

// handleMCPScoped: /mcp (Authorization: Bearer <chave>) e /mcp/{key} (chave na URL).
// Fora de /api/: quem autentica é a própria chave MCP.
func (s *server) handleMCPScoped(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	if key == "" {
		key, _ = strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	}
	if key == "" {
		key = r.Header.Get("X-API-Key")
	}
	key = strings.TrimSpace(key)
	if master := os.Getenv("WACALLS_API_KEY"); master != "" && key != "" &&
		subtle.ConstantTimeCompare([]byte(key), []byte(master)) == 1 {
		s.serveMCP(w, r, mcpScope{})
		return
	}
	if !strings.HasPrefix(key, mcpKeyPrefix) || s.sessions == nil || s.sessions.store == nil {
		w.Header().Set("WWW-Authenticate", `Bearer realm="astracalls-mcp"`)
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "chave MCP ausente ou inválida"})
		return
	}
	k, ok := s.sessions.store.lookupMCPKey(r.Context(), key)
	if !ok {
		w.Header().Set("WWW-Authenticate", `Bearer realm="astracalls-mcp"`)
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "chave MCP ausente ou inválida"})
		return
	}
	s.serveMCP(w, r, mcpScope{SessionID: k.SessionID, ReadOnly: k.ReadOnly})
}

// Rotas da sessão que uma chave MCP não alcança via api_request: administração
// da conta (parear, desconectar, webhooks, Chatwoot, SIP, proxy, chaves MCP).
var mcpScopedDenied = map[string]bool{
	"logout": true, "pair": true, "pair-code": true, "pair-passkey": true,
	"webhook": true, "webhooks": true, "chatwoot": true, "proxy": true, "mcp-keys": true,
	"sip": true, "sip-ext": true, "sip-agent": true, "recording": true,
}

// mcpScopedPathAllowed diz se api_request pode chamar este caminho com a chave da sessão.
func mcpScopedPathAllowed(sid, p string) bool {
	if path.Clean(p) != p {
		return false
	}
	rest, ok := strings.CutPrefix(p, "/api/sessions/"+sid+"/")
	if !ok || rest == "" {
		return false
	}
	first, _, _ := strings.Cut(rest, "/")
	return !mcpScopedDenied[first]
}

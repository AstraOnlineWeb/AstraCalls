package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func mcpPost(t *testing.T, h http.Handler, body string, hdr map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/mcp", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestMCPInitializeAndList(t *testing.T) {
	s := &server{}
	h := http.HandlerFunc(s.handleMCP)

	rec := mcpPost(t, h, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}`, nil)
	var init struct {
		Result struct {
			ProtocolVersion string         `json:"protocolVersion"`
			Capabilities    map[string]any `json:"capabilities"`
			ServerInfo      map[string]any `json:"serverInfo"`
		} `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &init); err != nil {
		t.Fatal(err, rec.Body.String())
	}
	if init.Result.ProtocolVersion != "2025-03-26" || init.Result.Capabilities["tools"] == nil || init.Result.ServerInfo["name"] != "astracalls" {
		t.Fatalf("initialize inesperado: %s", rec.Body.String())
	}

	// versão desconhecida -> a mais nova suportada
	rec = mcpPost(t, h, `{"jsonrpc":"2.0","id":2,"method":"initialize","params":{"protocolVersion":"1999-01-01"}}`, nil)
	if !strings.Contains(rec.Body.String(), mcpLatestProtocol) {
		t.Fatalf("negociação: %s", rec.Body.String())
	}

	// notificação: 202 sem corpo
	rec = mcpPost(t, h, `{"jsonrpc":"2.0","method":"notifications/initialized"}`, nil)
	if rec.Code != http.StatusAccepted || rec.Body.Len() != 0 {
		t.Fatalf("notificação: %d %q", rec.Code, rec.Body.String())
	}

	rec = mcpPost(t, h, `{"jsonrpc":"2.0","id":3,"method":"tools/list"}`, nil)
	var list struct {
		Result struct {
			Tools []struct {
				Name        string         `json:"name"`
				Description string         `json:"description"`
				InputSchema map[string]any `json:"inputSchema"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Result.Tools) != len(mcpTools) || len(mcpTools) < 40 {
		t.Fatalf("tools/list devolveu %d ferramentas", len(list.Result.Tools))
	}
	seen := map[string]bool{}
	for _, tl := range list.Result.Tools {
		if seen[tl.Name] {
			t.Errorf("ferramenta duplicada: %s", tl.Name)
		}
		seen[tl.Name] = true
		if tl.Description == "" || tl.InputSchema["type"] != "object" {
			t.Errorf("%s: descrição/esquema incompletos", tl.Name)
		}
		props, _ := tl.InputSchema["properties"].(map[string]any)
		req, _ := tl.InputSchema["required"].([]any)
		for _, r := range req {
			if _, ok := props[r.(string)]; !ok {
				t.Errorf("%s: required %q não está nas propriedades", tl.Name, r)
			}
		}
	}

	rec = mcpPost(t, h, `{"jsonrpc":"2.0","id":4,"method":"nada"}`, nil)
	if !strings.Contains(rec.Body.String(), "-32601") {
		t.Fatalf("método desconhecido: %s", rec.Body.String())
	}
	rec = mcpPost(t, h, `{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"nao_existe"}}`, nil)
	if !strings.Contains(rec.Body.String(), "-32602") {
		t.Fatalf("ferramenta desconhecida: %s", rec.Body.String())
	}

	// GET não abre stream
	req := httptest.NewRequest(http.MethodGet, "/api/mcp", nil)
	r2 := httptest.NewRecorder()
	h.ServeHTTP(r2, req)
	if r2.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET: %d", r2.Code)
	}
}

func TestMCPBuildCalls(t *testing.T) {
	build := func(name string, args map[string]any) (mcpCall, error) {
		tl, ok := mcpFindTool(name)
		if !ok {
			t.Fatalf("sem ferramenta %s", name)
		}
		return tl.Build(mcpArgs(args))
	}
	c, err := build("send_text", map[string]any{"to": "5561999998888", "text": "oi", "mentions": "5511, 5521"})
	if err != nil || c.Method != "POST" || c.Path != "/messages/text" {
		t.Fatalf("send_text: %+v %v", c, err)
	}
	if b := c.Body.(map[string]any); len(b["mentions"].([]string)) != 2 {
		t.Fatalf("mentions: %+v", b)
	}
	if _, err := build("send_text", map[string]any{"to": "55"}); err == nil {
		t.Fatal("send_text sem texto deveria falhar")
	}
	if _, err := build("send_image", map[string]any{"to": "55"}); err == nil {
		t.Fatal("imagem sem url/base64 deveria falhar")
	}
	c, _ = build("send_audio", map[string]any{"to": "55", "url": "https://x/a.ogg"})
	if c.Body.(map[string]any)["ptt"] != true {
		t.Fatal("áudio deveria ir como nota de voz por padrão")
	}
	c, _ = build("fake_call", map[string]any{"phone": "55", "seconds": "99"})
	if c.Body.(map[string]any)["duration_ms"] != 30000 {
		t.Fatalf("fake_call limita a 30 s: %+v", c.Body)
	}
	c, _ = build("update_group_participants", map[string]any{"groupId": "1@g.us", "action": "promote", "participants": []any{"55"}})
	if c.Path != "/groups/1@g.us/participants/promote" {
		t.Fatalf("participantes: %s", c.Path)
	}
	if _, err := build("update_group_participants", map[string]any{"groupId": "1@g.us", "action": "x", "participants": "55"}); err == nil {
		t.Fatal("ação inválida deveria falhar")
	}
	c, _ = build("search_messages", map[string]any{"q": "boleto", "chat": "5561", "limit": 5.0})
	if c.Query.Get("q") != "boleto" || c.Query.Get("chatId") != "5561" || c.Query.Get("limit") != "5" {
		t.Fatalf("search: %v", c.Query)
	}
	for _, p := range []string{"/api/mcp", "/livez", "/api/../x"} {
		if _, err := build("api_request", map[string]any{"method": "GET", "path": p}); err == nil {
			t.Errorf("api_request deveria recusar %s", p)
		}
	}
}

func TestMCPInvokeDispatchesToMux(t *testing.T) {
	mux := http.NewServeMux()
	var got string
	mux.HandleFunc("POST /api/sessions/{sid}/messages/text", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got = r.PathValue("sid") + " " + string(b)
		writeJSON(w, http.StatusOK, map[string]string{"id": "MSG1"})
	})
	mux.HandleFunc("GET /api/x", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such session"})
	})
	s := &server{apiMux: mux}
	req := httptest.NewRequest(http.MethodPost, "/api/mcp", nil)

	code, body := s.mcpInvoke(req, mcpCall{Method: "POST", Path: "/api/sessions/abc/messages/text", Body: map[string]any{"to": "55", "text": "oi"}})
	if code != 200 || !strings.Contains(string(body), "MSG1") || !strings.HasPrefix(got, "abc ") || !strings.Contains(got, `"text":"oi"`) {
		t.Fatalf("despacho: %d %s / %s", code, body, got)
	}
	tl, _ := mcpFindTool("api_request")
	res := s.mcpRunTool(req, tl, mcpArgs{"method": "GET", "path": "/api/x"}, mcpScope{})
	if res["isError"] != true || !strings.Contains(mcpJSON(res), "HTTP 404") {
		t.Fatalf("erro HTTP deveria virar isError: %+v", res)
	}
}

func TestWithAuthAcceptsBearer(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	h := withAuth(ok, "segredo", "widget")
	for hdr, want := range map[string]int{
		"Bearer segredo": http.StatusNoContent,
		"Bearer errado":  http.StatusUnauthorized,
		"Bearer widget":  http.StatusUnauthorized, // chave de widget não libera o MCP
	} {
		rec := mcpPost(t, h, `{}`, map[string]string{"Authorization": hdr})
		if rec.Code != want {
			t.Errorf("%s: %d, queria %d", hdr, rec.Code, want)
		}
	}
}

func TestMCPScopedKeys(t *testing.T) {
	key, hash := newMCPKey()
	if !strings.HasPrefix(key, mcpKeyPrefix) || len(key) != len(mcpKeyPrefix)+64 || mcpKeyHash(key) != hash {
		t.Fatalf("chave: %s", key)
	}
	k2, _ := newMCPKey()
	if k2 == key {
		t.Fatal("chaves repetidas")
	}

	sc := mcpScope{SessionID: "S1", ReadOnly: true}
	var nRO, nAll int
	for _, tl := range mcpTools {
		nAll++
		if tl.allowed(sc) {
			nRO++
			if !tl.ReadOnly {
				t.Errorf("%s liberada em chave somente leitura", tl.Name)
			}
		}
		props := tl.schema(mcpScope{SessionID: "S1"})["properties"].(map[string]any)
		if _, ok := props["session"]; ok {
			t.Errorf("%s: chave de sessão não deveria expor o parâmetro session", tl.Name)
		}
	}
	if nRO == 0 || nRO == nAll {
		t.Fatalf("filtro somente leitura: %d de %d", nRO, nAll)
	}
	if !strings.Contains(mcpInstructionsFor(mcpScope{SessionID: "S1"}), "único número") {
		t.Fatal("instruções do escopo não trocaram o texto de sessões")
	}

	for p, want := range map[string]bool{
		"/api/sessions/S1/profile":       true,
		"/api/sessions/S1/messages/text": true,
		"/api/sessions/S1":               false, // DELETE da sessão
		"/api/sessions/S1/":              false,
		"/api/sessions/S2/profile":       false, // outra sessão
		"/api/sessions/S1/logout":        false,
		"/api/sessions/S1/webhooks/abc":  false,
		"/api/sessions/S1/mcp-keys":      false,
		"/api/sessions/S1/../S2/profile": false,
		"/api/sessions/S1//profile":      false,
		"/api/sessions":                  false,
	} {
		if got := mcpScopedPathAllowed("S1", p); got != want {
			t.Errorf("%s: %v, queria %v", p, got, want)
		}
	}
}

func TestMCPScopedEndpointRejectsBadKey(t *testing.T) {
	s := &server{}
	for _, hdr := range []string{"", "Bearer qualquer", "Bearer acmcp_falsa"} {
		req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
		if hdr != "" {
			req.Header.Set("Authorization", hdr)
		}
		rec := httptest.NewRecorder()
		s.handleMCPScoped(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%q: %d", hdr, rec.Code)
		}
	}
	t.Setenv("WACALLS_API_KEY", "mestra")
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping"}`))
	req.Header.Set("Authorization", "Bearer mestra")
	rec := httptest.NewRecorder()
	s.handleMCPScoped(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("chave-mestra em /mcp: %d %s", rec.Code, rec.Body.String())
	}
}

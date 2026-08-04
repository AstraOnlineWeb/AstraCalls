package main

import (
	"net/http/httptest"
	"testing"
)

// A chave de widget só pode abrir a superfície mínima (resolve, eventos e
// operações de chamada); tudo que é administrativo tem de ficar de fora (#10).
func TestWidgetAllowedScope(t *testing.T) {
	allowed := []struct{ method, path string }{
		{"GET", "/api/events"},
		{"GET", "/api/chatwoot/resolve"},
		{"POST", "/api/sessions/abc/calls"},
		{"POST", "/api/sessions/abc/calls/xyz/webrtc"},
		{"POST", "/api/sessions/abc/calls/xyz/accept"},
		{"POST", "/api/sessions/abc/calls/xyz/reject"},
		{"DELETE", "/api/sessions/abc/calls/xyz"},
		{"POST", "/api/sessions/abc/calls/xyz/video/stop"},
	}
	for _, c := range allowed {
		r := httptest.NewRequest(c.method, c.path, nil)
		if !widgetAllowed(r) {
			t.Errorf("widget deveria poder %s %s", c.method, c.path)
		}
	}

	denied := []struct{ method, path string }{
		{"GET", "/api/sessions"},               // listar sessões
		{"POST", "/api/sessions"},              // criar sessão
		{"DELETE", "/api/sessions/abc"},        // apagar sessão
		{"POST", "/api/sessions/abc/messages/pix"}, // mandar mensagem
		{"PUT", "/api/sessions/abc/recording"},     // configurar gravação
		{"GET", "/api/chatwoot/config"},            // config do chatwoot
		{"POST", "/api/chatwoot/resolve"},          // resolve só por GET
	}
	for _, c := range denied {
		r := httptest.NewRequest(c.method, c.path, nil)
		if widgetAllowed(r) {
			t.Errorf("chave de widget NÃO deveria abrir %s %s", c.method, c.path)
		}
	}
}

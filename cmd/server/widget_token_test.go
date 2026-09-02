package main

import (
	"net/http/httptest"
	"testing"
	"time"
)

func TestWidgetTokenRoundTrip(t *testing.T) {
	t.Setenv("WACALLS_API_KEY", "mestra-xyz")
	tok := signWidgetToken(widgetTokenClaims{AccountID: 4, InboxID: 9, Exp: time.Now().Add(time.Hour).Unix()})
	c, ok := parseWidgetToken(tok)
	if !ok {
		t.Fatal("token válido deveria parsear")
	}
	if c.AccountID != 4 || c.InboxID != 9 {
		t.Errorf("claims erradas: %+v", c)
	}
}

func TestWidgetTokenRejects(t *testing.T) {
	t.Setenv("WACALLS_API_KEY", "mestra-xyz")
	// expirado
	if _, ok := parseWidgetToken(signWidgetToken(widgetTokenClaims{AccountID: 4, Exp: time.Now().Add(-time.Minute).Unix()})); ok {
		t.Error("token expirado não deveria valer")
	}
	// account inválida
	if _, ok := parseWidgetToken(signWidgetToken(widgetTokenClaims{AccountID: 0, Exp: time.Now().Add(time.Hour).Unix()})); ok {
		t.Error("token sem conta não deveria valer")
	}
	// assinatura de outra chave-mestra não vale
	good := signWidgetToken(widgetTokenClaims{AccountID: 4, Exp: time.Now().Add(time.Hour).Unix()})
	t.Setenv("WACALLS_API_KEY", "outra-mestra")
	if _, ok := parseWidgetToken(good); ok {
		t.Error("token assinado com outra mestra não deveria valer")
	}
	// lixo
	if _, ok := parseWidgetToken("wt1.abc.def"); ok {
		t.Error("token corrompido não deveria valer")
	}
}

func TestWidgetTokenAccountScope(t *testing.T) {
	c := widgetTokenClaims{AccountID: 4}
	// accountId na query bate
	r := httptest.NewRequest("GET", "/api/events?accountId=4&clientId=astrachat_acc4", nil)
	if !widgetTokenAccountMatches(r, c) {
		t.Error("accountId=4 deveria casar com token da conta 4")
	}
	// conta divergente
	r = httptest.NewRequest("GET", "/api/events?accountId=10", nil)
	if widgetTokenAccountMatches(r, c) {
		t.Error("accountId=10 NÃO deveria casar com token da conta 4")
	}
	// deriva do clientId quando não vem accountId
	r = httptest.NewRequest("GET", "/api/events?clientId=astrachat_acc4", nil)
	if !widgetTokenAccountMatches(r, c) {
		t.Error("clientId=astrachat_acc4 deveria casar com token da conta 4")
	}
}

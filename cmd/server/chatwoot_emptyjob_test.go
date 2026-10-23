package main

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// Mensagem sem nada a postar (ex.: protocolo vindo do celular da própria conta)
// não pode abrir contato/conversa no Chatwoot — virava ticket vazio.
func TestChatwootEmptyJobDoesNotOpenConversation(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	cfg := ChatwootConfig{URL: srv.URL, AccountID: 1, AccountToken: "x", InboxID: 1}
	s := &Session{}
	if err := s.execChatwootJob(cfg, cwJob{ChatID: "551931673777@s.whatsapp.net", Phone: "551931673777", SourceID: "A", Text: "  "}); err != nil {
		t.Fatalf("err = %v", err)
	}
	if n := hits.Load(); n != 0 {
		t.Fatalf("job vazio fez %d chamadas ao Chatwoot", n)
	}
	// com texto, segue tentando entregar (e falha no servidor de teste)
	if err := s.execChatwootJob(cfg, cwJob{ChatID: "551931673777@s.whatsapp.net", Phone: "551931673777", SourceID: "B", Text: "oi"}); err == nil || hits.Load() == 0 {
		t.Fatalf("job com texto não chamou o Chatwoot (err=%v hits=%d)", err, hits.Load())
	}
}

package main

import (
	"strings"
	"testing"
)

func TestPixFromButtonParams(t *testing.T) {
	// cobrança Pix com valor (offset 1000 -> value/1000)
	params := `{"reference_id":"ABC123","currency":"BRL",
		"total_amount":{"value":49900,"offset":1000},
		"payment_settings":[{"type":"pix_static_code","pix_static_code":{
			"merchant_name":"Loja Astra","key":"loja@astra.com","key_type":"EMAIL"}}]}`
	got := pixFromButtonParams(params)
	for _, want := range []string{"Cobrança via Pix", "Loja Astra", "Chave (email): loja@astra.com", "BRL 49.90", "ABC123"} {
		if !strings.Contains(got, want) {
			t.Fatalf("pix não contém %q. Saída:\n%s", want, got)
		}
	}
	// json inválido -> vazio
	if pixFromButtonParams("{nao é json}") != "" {
		t.Fatal("json inválido deveria dar vazio")
	}
}

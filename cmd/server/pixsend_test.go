package main

import (
	"strings"
	"testing"
)

func TestBuildPixBRCodeWithAmount(t *testing.T) {
	code := buildPixBRCode("loja@astra.com", "Loja Astra", "São Paulo", 49.90, "PEDIDO123")

	for _, want := range []string{
		"br.gov.bcb.pix",
		"0114loja@astra.com", // chave (len 14)
		"5303986",            // moeda BRL
		"540549.90",          // valor: id54 len05 "49.90"
		"5802BR",
		"5910LOJA ASTRA", // nome sem acento, maiúsculo (len 10)
		"6009SAO PAULO",  // cidade sem acento (len 09)
		"0509PEDIDO123",  // txid
	} {
		if !strings.Contains(code, want) {
			t.Errorf("faltou %q em %s", want, code)
		}
	}
	assertPixCRC(t, code)
}

func TestBuildPixBRCodeNoAmount(t *testing.T) {
	code := buildPixBRCode("11999998888", "", "", 0, "")
	if strings.Contains(code, "5405") || strings.Contains(code, "5406") {
		t.Errorf("não deveria conter campo de valor (54): %s", code)
	}
	if !strings.Contains(code, "5903PIX") { // nome default
		t.Errorf("nome default PIX ausente: %s", code)
	}
	if !strings.Contains(code, "6006BRASIL") { // cidade default
		t.Errorf("cidade default BRASIL ausente: %s", code)
	}
	if !strings.Contains(code, "0503***") { // txid default
		t.Errorf("txid default *** ausente: %s", code)
	}
	assertPixCRC(t, code)
}

// assertPixCRC confere que os 4 últimos dígitos (campo 63) batem com o CRC16-CCITT
// recalculado sobre o restante do payload.
func assertPixCRC(t *testing.T, code string) {
	t.Helper()
	if len(code) < 8 {
		t.Fatalf("code muito curto: %s", code)
	}
	body, got := code[:len(code)-4], code[len(code)-4:]
	h := "0123456789ABCDEF"
	crc := crc16CCITT(body)
	want := string([]byte{h[(crc>>12)&0xF], h[(crc>>8)&0xF], h[(crc>>4)&0xF], h[crc&0xF]})
	if got != want {
		t.Errorf("CRC inválido: got %s want %s (%s)", got, want, code)
	}
}

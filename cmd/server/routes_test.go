package main

import (
	"fmt"
	"strings"
	"testing"
)

// TestRoutesNoConflict monta o mux (sem deps reais) só para pegar, em teste, padrões
// de rota AMBÍGUOS que o net/http (Go 1.22+) rejeita com panic no startup — como o
// conflito /calls/group/{id}/end × /calls/{id}/video/{action}, que derrubava o
// serviço inteiro ao subir. Falha o teste se houver conflito de rota.
func TestRoutesNoConflict(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			msg := fmt.Sprint(r)
			if strings.Contains(msg, "conflicts with pattern") {
				t.Fatalf("rota conflitante registrada: %v", msg)
			}
			// Outro panic (deps nil fora do registro de rotas) não é o alvo deste teste.
		}
	}()
	_ = (&server{}).routes()
}

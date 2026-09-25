package transport

import (
	"testing"
	"time"
)

// TestConsentIntervalBounds valida o requisito da RFC 7675 (seção 5.1): cada
// intervalo entre checks de consent randomizado em 0.8–1.2x o período base, e
// nunca menor que 4s. Roda muitas amostras porque o valor é aleatório.
func TestConsentIntervalBounds(t *testing.T) {
	const (
		min = 4 * time.Second // 0.8 * 5s — piso obrigatório da RFC
		max = 6 * time.Second // 1.2 * 5s
	)
	sawLow, sawHigh := false, false
	for i := 0; i < 10000; i++ {
		d := consentInterval()
		if d < min {
			t.Fatalf("intervalo %v abaixo do piso de 4s exigido pela RFC 7675", d)
		}
		if d > max {
			t.Fatalf("intervalo %v acima de 1.2x o período base", d)
		}
		if d < min+400*time.Millisecond {
			sawLow = true
		}
		if d > max-400*time.Millisecond {
			sawHigh = true
		}
	}
	if !sawLow || !sawHigh {
		t.Fatalf("randomização não cobriu a faixa esperada (low=%v high=%v)", sawLow, sawHigh)
	}
}

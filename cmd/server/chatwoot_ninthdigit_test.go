package main

import "testing"

func TestBRNinthDigitVariant(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		// celular SEM o 9 (forma canônica/12 díg.) -> COM o 9 (13 díg.)
		{"cel sem 9 -> com 9", "556196878959", "5561996878959"},
		{"cel sem 9 (sub inicia 8)", "556181148453", "5561981148453"},
		// celular COM o 9 (13 díg.) -> SEM o 9 (12 díg.)
		{"cel com 9 -> sem 9", "5561996878959", "556196878959"},
		// com "+" e ruído: digitsOnly normaliza
		{"com mais e espacos", "+55 61 96878959", "5561996878959"},
		// fixo (assinante começa 2-5): sem variante
		{"fixo 2xxx", "556132211234", ""},
		{"fixo 3xxx", "551133211234", ""},
		// não-BR: vazio
		{"estrangeiro", "13235550123", ""},
		// tamanhos inesperados: vazio
		{"curto demais", "5561999", ""},
		{"longo demais", "55619968789591", ""},
		// assinante de 8 díg. começando com 1 (nem fixo nem celular): vazio
		{"assinante invalido inicia 1", "556112345678", ""},
		// 9 dígitos mas não começa com 9 (inesperado): vazio
		{"9 digitos sem 9 na frente", "5561812345678", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := brNinthDigitVariant(tc.in); got != tc.want {
				t.Fatalf("brNinthDigitVariant(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// Ida e volta: aplicar a variante duas vezes volta à forma original (celular BR).
func TestBRNinthDigitVariantRoundTrip(t *testing.T) {
	for _, canon := range []string{"556196878959", "5511987654321"} {
		v := brNinthDigitVariant(canon)
		if v == "" {
			t.Fatalf("esperava variante p/ %q", canon)
		}
		if back := brNinthDigitVariant(v); back != canon {
			t.Fatalf("round-trip %q -> %q -> %q (quebrou)", canon, v, back)
		}
	}
}

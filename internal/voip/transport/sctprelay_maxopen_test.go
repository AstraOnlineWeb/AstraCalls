package transport

import "testing"

func TestParseMaxOpenRelays(t *testing.T) {
	cases := map[string]int{
		"":    maxDialRelays, // padrão: todos os discados ficam abertos
		"abc": maxDialRelays,
		" 2 ": 2,
		"5":   5,
		"0":   1,
		"-3":  1,
	}
	for in, want := range cases {
		if got := parseMaxOpenRelays(in); got != want {
			t.Errorf("parseMaxOpenRelays(%q) = %d, want %d", in, got, want)
		}
	}
}

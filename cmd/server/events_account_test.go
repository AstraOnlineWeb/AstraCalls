package main

import "testing"

func TestAccountFromClientID(t *testing.T) {
	cases := []struct {
		cid  string
		want int
	}{
		{"astrachat_acc4", 4},
		{"astrachat_acc10", 10},
		{"astrachat_acc0", 0},
		{"astrachat_acc", 0},
		{"", 0},
		{"painel", 0},
		{"widget_123", 0},
		{"astrachat_accX", 0},
	}
	for _, c := range cases {
		if got := accountFromClientID(c.cid); got != c.want {
			t.Errorf("accountFromClientID(%q) = %d, quer %d", c.cid, got, c.want)
		}
	}
}

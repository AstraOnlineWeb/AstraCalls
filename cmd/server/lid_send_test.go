package main

import (
	"errors"
	"testing"
)

func TestIsLIDResolveErr(t *testing.T) {
	cases := []struct {
		err  error
		want bool
	}{
		{nil, false},
		{errors.New("no LID found for 5591984333508@s.whatsapp.net from server"), true},
		{errors.New("failed to get LID for PN 5591984333508@s.whatsapp.net: timeout"), true},
		{errors.New("some other network error"), false},
		{errors.New("context deadline exceeded"), false},
	}
	for _, c := range cases {
		if got := isLIDResolveErr(c.err); got != c.want {
			t.Errorf("isLIDResolveErr(%v) = %v, want %v", c.err, got, c.want)
		}
	}
}

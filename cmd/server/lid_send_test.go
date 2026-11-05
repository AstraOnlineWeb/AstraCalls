package main

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	"go.mau.fi/whatsmeow"
)

func TestIsLIDResolveErr(t *testing.T) {
	cases := []struct {
		err  error
		want bool
	}{
		{nil, false},
		{fmt.Errorf("%w: no LID found for 5591984333508@s.whatsapp.net from server", whatsmeow.ErrRecipientNotOnWhatsApp), true},
		{errors.New("no LID found for 5591984333508@s.whatsapp.net from server"), false},
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

func TestSendErrorResponse(t *testing.T) {
	cases := []struct {
		err      error
		wantCode int
		wantKey  string
	}{
		{fmt.Errorf("%w: no LID found for 5511999999999@s.whatsapp.net from server", whatsmeow.ErrRecipientNotOnWhatsApp), http.StatusUnprocessableEntity, "not_on_whatsapp"},
		{whatsmeow.ErrNoPermissionInGroup, http.StatusForbidden, "no_permission_in_group"},
		{errors.New("timeout"), http.StatusInternalServerError, ""},
	}
	for _, c := range cases {
		code, body := sendErrorResponse(c.err)
		if code != c.wantCode || body["code"] != c.wantKey {
			t.Errorf("sendErrorResponse(%v) = %d %v, want %d %q", c.err, code, body, c.wantCode, c.wantKey)
		}
	}
}

func TestConfigureClientUnifiedSession(t *testing.T) {
	cli := &whatsmeow.Client{}
	configureClient(cli)
	if cli.UnifiedSessionInterval != unifiedSessionInterval || unifiedSessionInterval <= 0 {
		t.Fatalf("UnifiedSessionInterval = %v, want %v (>0)", cli.UnifiedSessionInterval, unifiedSessionInterval)
	}
}

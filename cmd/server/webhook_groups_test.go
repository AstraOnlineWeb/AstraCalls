package main

import "testing"

func TestIsGroupWebhookEvent(t *testing.T) {
	cases := []struct {
		event string
		data  any
		want  bool
	}{
		{"group_participants", map[string]any{}, true},
		{"message", map[string]any{"isGroup": true, "chat": "123@g.us"}, true},
		{"message", map[string]any{"isGroup": false, "chat": "5511999999999@s.whatsapp.net"}, false},
		{"receipt", map[string]any{"chat": "120363000000000000@g.us"}, true},
		{"receipt", map[string]any{"chat": "81544853061771@lid"}, false},
		{"form_response", map[string]any{"from": "120363000000000000@g.us"}, true},
		{"session_status", map[string]any{"status": "open"}, false},
		{"broadcast", "x", false},
	}
	for _, c := range cases {
		if got := isGroupWebhookEvent(c.event, c.data); got != c.want {
			t.Errorf("%s %v: got %v want %v", c.event, c.data, got, c.want)
		}
	}
}

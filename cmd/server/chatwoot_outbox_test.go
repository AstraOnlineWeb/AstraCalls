package main

import (
	"encoding/json"
	"testing"
	"time"
)

func TestOutboxBackoff(t *testing.T) {
	cases := map[int]time.Duration{
		1: 60 * time.Second,
		2: 120 * time.Second,
		3: 240 * time.Second,
		4: 480 * time.Second,
		5: 960 * time.Second,
		6: 30 * time.Minute, // 1920s > teto -> capado
		7: 30 * time.Minute,
		20: 30 * time.Minute,
	}
	for attempts, want := range cases {
		if got := outboxBackoff(attempts); got != want {
			t.Errorf("outboxBackoff(%d) = %s, want %s", attempts, got, want)
		}
	}
}

func TestCwJobRoundTrip(t *testing.T) {
	orig := cwJob{
		ChatID:   "5511999@s.whatsapp.net",
		Phone:    "5511999",
		Name:     "Fulano",
		Prefix:   "*Fulano*:\n",
		Text:     "oi",
		SourceID: "ABC123",
		MsgRaw:   json.RawMessage(`{"conversation":"oi"}`),
	}
	b, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got cwJob
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !got.hasMedia() {
		t.Errorf("hasMedia() = false, esperava true (MsgRaw preservado)")
	}
	if got.SourceID != orig.SourceID || got.Text != orig.Text || got.ChatID != orig.ChatID {
		t.Errorf("round-trip perdeu campos: %+v", got)
	}
}

func TestCwJobNoMedia(t *testing.T) {
	j := cwJob{Text: "só texto", SourceID: "X"}
	b, _ := json.Marshal(j)
	// sem mídia, o campo msg deve ser omitido (omitempty)
	if string(b) == "" || containsKey(b, `"msg"`) {
		t.Errorf("job sem mídia não deveria serializar msg: %s", b)
	}
	if j.hasMedia() {
		t.Errorf("hasMedia() = true, esperava false")
	}
}

func containsKey(b []byte, key string) bool {
	s := string(b)
	for i := 0; i+len(key) <= len(s); i++ {
		if s[i:i+len(key)] == key {
			return true
		}
	}
	return false
}

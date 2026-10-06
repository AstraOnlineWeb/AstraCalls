package media

import "testing"

func TestMediaClockNilSafe(t *testing.T) {
	var c *MediaClock
	if got := c.TimestampFor(16000); got != 0 {
		t.Fatalf("nil clock TimestampFor = %d, want 0", got)
	}
}

func TestMediaClockStartsNearZero(t *testing.T) {
	c := NewMediaClock()
	// Logo após a origem, o timestamp é perto de zero (muito menor que um start
	// aleatório do espaço de 32 bits). Tolerância folgada pra não ser flaky.
	if ts := c.TimestampFor(90000); ts > 90000 { // < ~1s de vídeo
		t.Fatalf("TimestampFor(90000) logo após origem = %d, esperado perto de 0", ts)
	}
}

func TestRtpSessionSetTimestamp(t *testing.T) {
	s := NewWhatsAppOpusSession(0x1234)
	s.SetTimestamp(0)
	pkt := s.CreatePacketWithDuration([]byte{0x00}, 960, false)
	if pkt.Header.Timestamp != 0 {
		t.Fatalf("1º pacote ts = %d, want 0 após SetTimestamp(0)", pkt.Header.Timestamp)
	}
	pkt2 := s.CreatePacketWithDuration([]byte{0x00}, 960, false)
	if pkt2.Header.Timestamp != 960 {
		t.Fatalf("2º pacote ts = %d, want 960", pkt2.Header.Timestamp)
	}
}

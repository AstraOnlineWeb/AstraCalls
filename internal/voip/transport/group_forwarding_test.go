package transport

import (
	"bytes"
	"testing"
)

func TestUnwrapGroupForwardingPacket(t *testing.T) {
	rtp := make([]byte, 12+20)
	rtp[0] = 0x80
	rtp[1] = 97
	// sem marcador: passa intacto
	if got, wrapped, valid := UnwrapGroupForwardingPacket(rtp); !valid || wrapped || !bytes.Equal(got, rtp) {
		t.Fatalf("unwrapped plain packet changed: wrapped=%v valid=%v", wrapped, valid)
	}
	for kind, hdr := range map[byte]int{2: 8, 4: 12, 7: 18} {
		pkt := append(make([]byte, hdr), rtp...)
		pkt[0], pkt[1] = 0x09, kind
		got, wrapped, valid := UnwrapGroupForwardingPacket(pkt)
		if !valid || !wrapped || !bytes.Equal(got, rtp) {
			t.Fatalf("kind %d: wrapped=%v valid=%v equal=%v", kind, wrapped, valid, bytes.Equal(got, rtp))
		}
		if ClassifyGroupRelayPacket(got) != GroupRelayRtp {
			t.Fatalf("kind %d: inner not classified as RTP", kind)
		}
	}
	// tipo desconhecido / curto demais = inválido
	if _, _, valid := UnwrapGroupForwardingPacket([]byte{0x09, 3, 0x80}); valid {
		t.Fatal("unknown header kind accepted")
	}
	if _, _, valid := UnwrapGroupForwardingPacket(append([]byte{0x09, 2}, make([]byte, 10)...)); valid {
		t.Fatal("short wrapped packet accepted")
	}
}

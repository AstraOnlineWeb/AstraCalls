package main

import (
	"net"
	"testing"
	"time"

	"github.com/pion/rtp"
)

// Ponte ao vivo: rajadas irregulares do WhatsApp saem como pacotes de 20 ms
// (160 amostras) num ritmo constante, e a fila não passa do teto ao vivo.
func TestLivePacerSends20msPackets(t *testing.T) {
	pbx, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer pbx.Close()
	b, err := NewSIPRTPBridge("t", pbx.LocalAddr().String(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	b.StartLivePacer()

	// rajada de 1,2 s de áudio de uma vez (16 kHz)
	_ = b.WritePCM(make([]float32, 16000*12/10))
	b.mu.Lock()
	q := len(b.outQueue)
	b.mu.Unlock()
	if q > sipLiveMaxSamples {
		t.Fatalf("fila ao vivo deveria ter no máx %d amostras, tem %d", sipLiveMaxSamples, q)
	}

	buf := make([]byte, 1500)
	_ = pbx.SetReadDeadline(time.Now().Add(2 * time.Second))
	var last time.Time
	for i := 0; i < 10; i++ {
		n, _, err := pbx.ReadFromUDP(buf)
		if err != nil {
			t.Fatal(err)
		}
		var p rtp.Packet
		if err := p.Unmarshal(buf[:n]); err != nil {
			t.Fatal(err)
		}
		if len(p.Payload) != 160 {
			t.Fatalf("pacote %d com %d amostras, esperado 160", i, len(p.Payload))
		}
		now := time.Now()
		if i > 1 && now.Sub(last) > 60*time.Millisecond {
			t.Fatalf("intervalo entre pacotes %v, esperado ~20ms", now.Sub(last))
		}
		last = now
	}
}

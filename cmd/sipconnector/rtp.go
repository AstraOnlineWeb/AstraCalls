package main

import (
	"errors"
	"math/rand/v2"
	"net"
	"sync"
	"time"

	"github.com/pion/rtp"
)

// rtpLeg é a perna de mídia com o PABX: um socket UDP numa faixa fixa (para dar
// para publicar no Swarm em mode=host), RTP simétrico (o destino passa a ser de onde
// os pacotes chegam, o que atravessa NAT do lado do PABX) e G.711.
type rtpLeg struct {
	conn *net.UDPConn
	pt   uint8

	mu     sync.Mutex
	remote *net.UDPAddr
	seq    uint16
	ts     uint32
	ssrc   uint32
	closed bool

	onPayload func(payload []byte, pt uint8) // áudio vindo do PABX
}

type portAllocator struct {
	mu       sync.Mutex
	min, max int
	next     int
}

func newPortAllocator(min, max int) *portAllocator {
	return &portAllocator{min: min, max: max, next: min}
}

// listen tenta as portas da faixa a partir de um cursor rotativo (RTP em porta par,
// como manda a convenção; a ímpar seguinte fica livre para RTCP).
func (a *portAllocator) listen() (*net.UDPConn, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	span := a.max - a.min + 1
	for i := 0; i < span; i += 2 {
		p := a.next
		a.next += 2
		if a.next > a.max {
			a.next = a.min
		}
		conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4zero, Port: p})
		if err == nil {
			return conn, nil
		}
	}
	return nil, errors.New("faixa de portas RTP esgotada")
}

func newRTPLeg(ports *portAllocator) (*rtpLeg, error) {
	conn, err := ports.listen()
	if err != nil {
		return nil, err
	}
	l := &rtpLeg{
		conn: conn,
		pt:   ptPCMU,
		seq:  uint16(rand.IntN(65535)),
		ts:   rand.Uint32(),
		ssrc: rand.Uint32(),
	}
	go l.readLoop()
	return l, nil
}

func (l *rtpLeg) localPort() int {
	return l.conn.LocalAddr().(*net.UDPAddr).Port
}

func (l *rtpLeg) setRemote(addr string) {
	if addr == "" {
		return
	}
	a, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		return
	}
	l.mu.Lock()
	l.remote = a
	l.mu.Unlock()
}

func (l *rtpLeg) setCodec(pt uint8) {
	l.mu.Lock()
	l.pt = pt
	l.mu.Unlock()
}

func (l *rtpLeg) codec() uint8 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.pt
}

// send manda um quadro G.711 (já no codec negociado) para o PABX.
func (l *rtpLeg) send(payload []byte) {
	l.mu.Lock()
	if l.closed || l.remote == nil || len(payload) == 0 {
		l.mu.Unlock()
		return
	}
	pkt := rtp.Packet{
		Header: rtp.Header{
			Version: 2, PayloadType: l.pt, SequenceNumber: l.seq, Timestamp: l.ts, SSRC: l.ssrc,
		},
		Payload: payload,
	}
	l.seq++
	l.ts += uint32(len(payload)) // G.711: 1 byte = 1 amostra a 8 kHz
	remote := l.remote
	l.mu.Unlock()

	if raw, err := pkt.Marshal(); err == nil {
		_, _ = l.conn.WriteToUDP(raw, remote)
	}
}

func (l *rtpLeg) readLoop() {
	buf := make([]byte, 1500)
	for {
		_ = l.conn.SetReadDeadline(time.Now().Add(time.Second))
		n, addr, err := l.conn.ReadFromUDP(buf)
		if err != nil {
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				l.mu.Lock()
				closed := l.closed
				l.mu.Unlock()
				if closed {
					return
				}
				continue
			}
			return
		}
		var pkt rtp.Packet
		if pkt.Unmarshal(buf[:n]) != nil {
			continue
		}
		if pkt.PayloadType != ptPCMU && pkt.PayloadType != ptPCMA {
			continue // DTMF (telephone-event) e comfort noise: ignorados
		}
		l.mu.Lock()
		l.remote = addr // RTP simétrico
		cb := l.onPayload
		l.mu.Unlock()
		if cb != nil {
			cb(append([]byte(nil), pkt.Payload...), pkt.PayloadType)
		}
	}
}

func (l *rtpLeg) close() {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return
	}
	l.closed = true
	l.mu.Unlock()
	_ = l.conn.Close()
}

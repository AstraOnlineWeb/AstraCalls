package main

import (
	"fmt"
	"log/slog"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/pion/rtp"
)

// ===== G.711 u-law codec =====

func linearToUlaw(sample int16) byte {
	sign := (sample >> 8) & 0x80
	if sign != 0 {
		sample = -sample
	}
	if sample > 32635 {
		sample = 32635
	}
	sample += 0x84
	exponent := uint16(7)
	for mask := uint16(0x4000); (uint16(sample) & mask) == 0; mask >>= 1 {
		exponent--
	}
	mantissa := (sample >> (exponent + 3)) & 0x0F
	ulaw := byte(sign | int16(exponent<<4) | int16(mantissa))
	return ^ulaw
}

func ulawToLinear(ulaw byte) int16 {
	ulaw = ^ulaw
	sign := (ulaw & 0x80)
	exponent := (ulaw & 0x70) >> 4
	data := ulaw & 0x0F
	sample := int16(data<<3) + 0x84
	sample <<= exponent
	if sign != 0 {
		return -sample
	}
	return sample
}

// SIPRTPBridge faz a ponte de áudio entre o RTP do SIP (G.711 u-law 8kHz)
// e o PCM 16kHz do WhatsApp. Recebe RTP do softphone/PBX, decodifica para
// float32 16kHz e injeta no encoder do WhatsApp; e no sentido inverso pega
// o PCM 16kHz do WhatsApp, reduz para 8kHz u-law e envia por RTP.
type SIPRTPBridge struct {
	waCallID  string
	conn      *net.UDPConn
	remote    *net.UDPAddr
	closeChan chan struct{}
	closed    bool
	log       *slog.Logger

	OnCapturedPCM func(pcm []float32)
	OnActive      func()
	OnEnded       func(reason string)

	mu         sync.Mutex
	activeSent bool
	endedSent  bool
	seqOut     uint16
	tsOut      uint32
	ssrc       uint32

	// diagnóstico (WhatsApp->SIP): conta chamadas de WritePCM e pacotes de fato
	// enviados, p/ diagnosticar o sentido de áudio que não passava.
	wroteFirst bool
	writeCalls uint64
	writeSent  uint64
	recvFirst  bool
}

func NewSIPRTPBridge(waCallID, remoteRTP string, log *slog.Logger) (*SIPRTPBridge, error) {
	// remoteRTP pode vir vazio (chamada WhatsApp->SIP, onde o destino só é
	// conhecido após o 200 OK do softphone) — nesse caso fica definido depois.
	var addr *net.UDPAddr
	if remoteRTP != "" {
		a, err := net.ResolveUDPAddr("udp", remoteRTP)
		if err != nil {
			return nil, fmt.Errorf("invalid remote RTP address: %w", err)
		}
		addr = a
	}

	conn, err := listenRTP()
	if err != nil {
		return nil, fmt.Errorf("listen RTP: %w", err)
	}

	if log == nil {
		log = slog.Default()
	}
	br := &SIPRTPBridge{
		waCallID:  waCallID,
		conn:      conn,
		remote:    addr,
		closeChan: make(chan struct{}),
		seqOut:    1000,
		ssrc:      1234567,
		log:       log,
	}
	br.log.Info("sip rtp bridge criada", "wa_call_id", waCallID, "local_port", br.LocalPort(), "remote", remoteRTP)

	go br.readLoop()
	return br, nil
}

// listenRTP abre o socket UDP do RTP dentro de uma FAIXA de portas fixa
// (WACALLS_RTP_PORT_MIN..MAX, padrão 40000..40049) em vez de porta efêmera —
// assim dá pra publicar essa faixa no Docker Swarm (mode=host) e o áudio chega.
// Cai para porta efêmera (Port: 0) se a faixa estiver toda ocupada.
func listenRTP() (*net.UDPConn, error) {
	min, max := rtpPortRange()
	for p := min; p <= max; p++ {
		conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(0, 0, 0, 0), Port: p})
		if err == nil {
			return conn, nil
		}
	}
	return net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(0, 0, 0, 0), Port: 0})
}

func rtpPortRange() (min, max int) {
	min = envInt("WACALLS_RTP_PORT_MIN", 40000)
	max = envInt("WACALLS_RTP_PORT_MAX", 40049)
	if min <= 0 || min > 65535 {
		min = 40000
	}
	if max < min || max > 65535 {
		max = min + 49
	}
	return min, max
}

func (b *SIPRTPBridge) LocalAddr() *net.UDPAddr {
	return b.conn.LocalAddr().(*net.UDPAddr)
}

func (b *SIPRTPBridge) LocalPort() int {
	return b.LocalAddr().Port
}

// readLoop recebe RTP do softphone, decodifica PCMU -> float32 16kHz e injeta imediatamente.
func (b *SIPRTPBridge) readLoop() {
	buf := make([]byte, 1500)
	for {
		select {
		case <-b.closeChan:
			return
		default:
		}
		_ = b.conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
		n, addr, err := b.conn.ReadFromUDP(buf)
		if err != nil {
			if strings.Contains(err.Error(), "timeout") || strings.Contains(err.Error(), "deadline") {
				continue
			}
			return
		}

		b.mu.Lock()
		b.remote = addr
		firstRecv := !b.recvFirst
		b.recvFirst = true
		b.mu.Unlock()
		if firstRecv {
			b.log.Info("sip rtp: primeiro pacote RECEBIDO do PBX (SIP->WhatsApp)", "wa_call_id", b.waCallID, "from", addr.String())
		}

		var packet rtp.Packet
		if err := packet.Unmarshal(buf[:n]); err != nil {
			continue // Not RTP
		}

		if packet.PayloadType == 0 { // PCMU 8kHz
			pcm := make([]float32, 0, len(packet.Payload)*2)
			for _, u := range packet.Payload {
				s := ulawToLinear(u)
				fs := float32(s) / 32768.0
				// 8k -> 16k na proporção 1:2 (duplicação simples de amostra).
				pcm = append(pcm, fs, fs)
			}

			// Injeta imediatamente no encoder do WhatsApp, que tem o próprio timer.
			if b.OnCapturedPCM != nil {
				b.OnCapturedPCM(pcm)
			}
		}
	}
}

// WritePCM é chamada quando o WhatsApp entrega áudio (frames de 10-60ms).
func (b *SIPRTPBridge) WritePCM(pcm []float32) error {
	// Downsample 16kHz -> 8kHz pegando uma amostra a cada duas.
	payload := make([]byte, 0, len(pcm)/2+1)

	for i := 0; i < len(pcm); i += 2 {
		s := pcm[i]
		if s > 1.0 {
			s = 1.0
		} else if s < -1.0 {
			s = -1.0
		}
		i16 := int16(s * 32767)
		payload = append(payload, linearToUlaw(i16))
	}

	if len(payload) == 0 {
		return nil
	}

	b.mu.Lock()
	pkt := rtp.Packet{
		Header: rtp.Header{
			Version:        2,
			PayloadType:    0,
			SequenceNumber: b.seqOut,
			Timestamp:      b.tsOut,
			SSRC:           b.ssrc,
		},
		Payload: payload,
	}
	b.seqOut++
	// timestamp avança pelo número de amostras 8kHz.
	b.tsOut += uint32(len(payload))
	remote := b.remote
	b.writeCalls++
	calls := b.writeCalls
	firstWrite := !b.wroteFirst
	b.wroteFirst = true
	b.mu.Unlock()

	if firstWrite {
		b.log.Info("sip rtp: primeiro WritePCM (WhatsApp->SIP) recebido do peer", "wa_call_id", b.waCallID, "remote", addrStr(remote))
	}

	if remote == nil {
		// destino RTP ainda não conhecido (aguardando 200 OK do softphone).
		if calls%250 == 1 {
			b.log.Warn("sip rtp: WhatsApp->SIP sem destino RTP (remote nil) — áudio do peer descartado", "wa_call_id", b.waCallID, "write_calls", calls)
		}
		return nil
	}

	raw, err := pkt.Marshal()
	if err != nil {
		return err
	}
	n, err := b.conn.WriteToUDP(raw, remote)
	if err != nil {
		b.log.Warn("sip rtp: erro enviando RTP p/ o PBX", "wa_call_id", b.waCallID, "remote", remote.String(), "err", err)
		return err
	}
	b.mu.Lock()
	b.writeSent++
	sent := b.writeSent
	b.mu.Unlock()
	if sent == 1 || sent%500 == 0 {
		b.log.Info("sip rtp: enviando RTP p/ o PBX (WhatsApp->SIP)", "wa_call_id", b.waCallID, "remote", remote.String(), "pkts_sent", sent, "bytes", n)
	}
	return nil
}

func addrStr(a *net.UDPAddr) string {
	if a == nil {
		return "<nil>"
	}
	return a.String()
}

func (b *SIPRTPBridge) Close() {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return
	}
	b.closed = true
	b.mu.Unlock()
	close(b.closeChan)
	_ = b.conn.Close()
}

func (b *SIPRTPBridge) NotifyActive() {
	b.mu.Lock()
	if b.activeSent {
		b.mu.Unlock()
		return
	}
	b.activeSent = true
	b.mu.Unlock()

	if b.OnActive != nil {
		b.OnActive()
	}
}

func (b *SIPRTPBridge) NotifyEnded(reason string) {
	b.mu.Lock()
	if b.endedSent {
		b.mu.Unlock()
		return
	}
	b.endedSent = true
	b.mu.Unlock()

	if b.OnEnded != nil {
		b.OnEnded(reason)
	}
}

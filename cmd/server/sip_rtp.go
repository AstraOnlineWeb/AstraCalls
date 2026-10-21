package main

import (
	"fmt"
	"log/slog"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/pion/rtp"

	"wacalls/internal/voip/call"
)

// sipDebugEnabled liga os logs de diagnóstico do SIP/ponte de áudio. Desligado por
// padrão; habilite com WACALLS_SIP_DEBUG=1 na stack só para depurar.
var sipDebugEnabled = func() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("WACALLS_SIP_DEBUG"))) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}()

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
	sample := (int16(data)<<3 + 0x84) << exponent
	sample -= 0x84 // tira o viés do encoder (sem isso o silêncio vira +132)
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
	// OnDTMF recebe os dígitos de telephone-event (RFC 4733), um por tecla.
	OnDTMF func(digit string)

	// codec negociado (PCMU/PCMA) e PT do telephone-event (0 = sem DTMF).
	pt       uint8
	dtmfPT   uint8
	lastDTMF uint32 // timestamp RTP do último evento DTMF entregue (dedupe)
	dtmfSeen bool
	up       upsampler8to16
	down     downsampler16to8

	// paced: em vez de mandar um pacote por WritePCM, enfileira e um relógio
	// próprio envia 160 amostras (20 ms) por tick, com silêncio quando a fila
	// esvazia. Necessário quando a fonte (agente/TTS) manda em rajadas.
	paced    bool
	outQueue []int16
	// maxQueue: teto da fila ritmada em amostras (0 = sipPacedMaxSamples). Ponte
	// ao vivo usa um teto curto para o atraso não acumular.
	maxQueue int

	// held: em espera (transferência) — o pacer ignora a fila e toca a música
	// de espera (moh, 8 kHz) ou silêncio; o áudio novo do agente é descartado.
	held   bool
	moh    []int16
	mohPos int

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
	if sipDebugEnabled {
		br.log.Info("sip rtp bridge criada", "wa_call_id", waCallID, "local_port", br.LocalPort(), "remote", remoteRTP)
	}

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

// readLoop recebe RTP do softphone, decodifica G.711 -> float32 16kHz e injeta imediatamente.
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
		if firstRecv && sipDebugEnabled {
			b.log.Info("sip rtp: primeiro pacote RECEBIDO do PBX (SIP->WhatsApp)", "wa_call_id", b.waCallID, "from", addr.String())
		}

		var packet rtp.Packet
		if err := packet.Unmarshal(buf[:n]); err != nil {
			continue // Not RTP
		}

		b.mu.Lock()
		pt, dtmfPT := b.pt, b.dtmfPT
		b.mu.Unlock()

		if dtmfPT != 0 && packet.PayloadType == dtmfPT {
			b.handleDTMF(&packet)
			continue
		}
		if packet.PayloadType == pt {
			in := make([]float32, len(packet.Payload))
			for i, c := range packet.Payload {
				in[i] = float32(g711Decode(pt, c)) / 32768.0
			}
			b.mu.Lock()
			pcm := b.up.process(in)
			cb := b.OnCapturedPCM
			b.mu.Unlock()

			// Injeta imediatamente no consumidor (encoder do WhatsApp ou agente).
			if cb != nil {
				cb(pcm)
			}
		}
	}
}

// handleDTMF entrega cada tecla uma única vez (o evento é repetido em vários
// pacotes com o mesmo timestamp até o bit de fim).
func (b *SIPRTPBridge) handleDTMF(p *rtp.Packet) {
	if len(p.Payload) < 4 {
		return
	}
	b.mu.Lock()
	if b.dtmfSeen && b.lastDTMF == p.Timestamp {
		b.mu.Unlock()
		return
	}
	b.dtmfSeen = true
	b.lastDTMF = p.Timestamp
	cb := b.OnDTMF
	b.mu.Unlock()
	if d := dtmfDigit(p.Payload[0]); d != "" && cb != nil {
		cb(d)
	}
}

// SetHandlers troca os callbacks de áudio/DTMF com o leitor RTP já rodando.
func (b *SIPRTPBridge) SetHandlers(onPCM func([]float32), onDTMF func(string)) {
	b.mu.Lock()
	b.OnCapturedPCM = onPCM
	b.OnDTMF = onDTMF
	b.mu.Unlock()
}

// SetCodec define o codec G.711 (PT 0 ou 8) e o PT do telephone-event.
func (b *SIPRTPBridge) SetCodec(pt, dtmfPT uint8) {
	b.mu.Lock()
	b.pt, b.dtmfPT = pt, dtmfPT
	b.mu.Unlock()
}

// StartPacer liga o envio ritmado (20 ms) — ver campo paced.
func (b *SIPRTPBridge) StartPacer() {
	b.mu.Lock()
	if b.paced {
		b.mu.Unlock()
		return
	}
	b.paced = true
	b.mu.Unlock()
	go b.pacerLoop()
}

// StartLivePacer liga o envio ritmado para uma ponte AO VIVO (WhatsApp <-> PBX):
// o áudio do WhatsApp chega em rajadas irregulares (frames de 60 ms, com buracos
// de centenas de ms); mandado direto virava pacotes de 10–180 ms fora do ptime
// 20 e o PBX tocava com ruído/"ring" de fundo. A fila fica curta (sipLiveMaxSamples)
// para não acumular atraso.
func (b *SIPRTPBridge) StartLivePacer() {
	b.mu.Lock()
	b.maxQueue = sipLiveMaxSamples
	b.mu.Unlock()
	b.StartPacer()
}

// ClearOutput descarta o áudio ainda não enviado (barge-in do agente).
func (b *SIPRTPBridge) ClearOutput() {
	b.mu.Lock()
	b.outQueue = b.outQueue[:0]
	b.mu.Unlock()
}

// SetHold liga/desliga a espera (só no modo ritmado): com moh toca a música de
// espera para quem ligou; sem, silêncio. Entrar em espera descarta a fila.
func (b *SIPRTPBridge) SetHold(held, moh bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.held = held
	b.outQueue = b.outQueue[:0]
	b.moh = nil
	if held && moh {
		var d downsampler16to8
		low := d.process(call.MOHLoop())
		b.moh = make([]int16, len(low))
		for i, v := range low {
			b.moh[i] = floatToInt16(v)
		}
		b.mohPos = 0
	}
}

// limite da fila de saída ritmada: 60 s de áudio a 8 kHz.
const sipPacedMaxSamples = 8000 * 60

// limite da fila ritmada numa ponte ao vivo: 300 ms (absorve o jitter do relay
// sem deixar o atraso crescer).
const sipLiveMaxSamples = 8000 * 3 / 10

func (b *SIPRTPBridge) pacerLoop() {
	const frame = 160 // 20 ms a 8 kHz
	t := time.NewTicker(20 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-b.closeChan:
			return
		case <-t.C:
		}
		b.mu.Lock()
		samples := make([]int16, frame)
		if b.held {
			for i := 0; i < frame && len(b.moh) > 0; i++ {
				samples[i] = b.moh[b.mohPos]
				b.mohPos = (b.mohPos + 1) % len(b.moh)
			}
		} else {
			n := copy(samples, b.outQueue)
			b.outQueue = b.outQueue[:copy(b.outQueue, b.outQueue[n:])]
		}
		b.mu.Unlock()
		_ = b.sendSamples(samples)
	}
}

// WritePCM recebe PCM 16 kHz (do WhatsApp ou do agente), reduz para 8 kHz e
// envia por RTP — direto, ou pela fila ritmada quando StartPacer foi chamado.
func (b *SIPRTPBridge) WritePCM(pcm []float32) error {
	b.mu.Lock()
	low := b.down.process(pcm)
	samples := make([]int16, len(low))
	for i, v := range low {
		samples[i] = floatToInt16(v)
	}
	if b.paced {
		if b.held {
			b.mu.Unlock()
			return nil
		}
		b.outQueue = append(b.outQueue, samples...)
		limit := b.maxQueue
		if limit <= 0 {
			limit = sipPacedMaxSamples
		}
		if over := len(b.outQueue) - limit; over > 0 {
			b.outQueue = b.outQueue[over:]
		}
		b.mu.Unlock()
		return nil
	}
	b.mu.Unlock()
	return b.sendSamples(samples)
}

// sendSamples codifica amostras 8 kHz no codec negociado e envia um pacote RTP.
func (b *SIPRTPBridge) sendSamples(samples []int16) error {
	if len(samples) == 0 {
		return nil
	}

	b.mu.Lock()
	pt := b.pt
	payload := make([]byte, len(samples))
	for i, v := range samples {
		payload[i] = g711Encode(pt, v)
	}
	pkt := rtp.Packet{
		Header: rtp.Header{
			Version:        2,
			PayloadType:    pt,
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

	if firstWrite && sipDebugEnabled {
		b.log.Info("sip rtp: primeiro envio de áudio p/ o SIP", "wa_call_id", b.waCallID, "remote", addrStr(remote))
	}

	if remote == nil {
		// destino RTP ainda não conhecido (aguardando 200 OK do softphone).
		if sipDebugEnabled && calls%250 == 1 {
			b.log.Warn("sip rtp: sem destino RTP (remote nil) — áudio descartado", "wa_call_id", b.waCallID, "write_calls", calls)
		}
		return nil
	}

	raw, err := pkt.Marshal()
	if err != nil {
		return err
	}
	n, err := b.conn.WriteToUDP(raw, remote)
	if err != nil {
		if calls%500 == 1 { // erro real, mas evita spam por pacote
			b.log.Warn("sip rtp: erro enviando RTP p/ o PBX", "wa_call_id", b.waCallID, "remote", remote.String(), "err", err)
		}
		return err
	}
	if sipDebugEnabled {
		b.mu.Lock()
		b.writeSent++
		sent := b.writeSent
		b.mu.Unlock()
		if sent == 1 || sent%500 == 0 {
			b.log.Info("sip rtp: enviando RTP p/ o PBX", "wa_call_id", b.waCallID, "remote", remote.String(), "pkts_sent", sent, "bytes", n)
		}
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

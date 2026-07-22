package video

import (
	"fmt"
	"log/slog"
	"sync"
	"time"

	"wacalls/internal/voip/core"
	"wacalls/internal/voip/media"
	"wacalls/internal/voip/transport"
)

const (
	rtpStepSamples      = 90000 / 15
	congestionDropBytes = 48 * 1024
	slotWord            = 2
)

var (
	// Plano completo de 9 streams do relay (WhatsApp Web): 3 grupos × 3 camadas.
	// Ordem = posições do template 0x4024. Slot 2 (vídeo) = grupo1/camada0.
	callSlots       = []uint32{0, 1, 4, 2, 3, 5, 7, 8, 6}
	annexBStartCode = []byte{0, 0, 0, 1}
)

type Relay interface {
	Broadcast(data []byte)
	BufferedAmount() uint64
	HasConnection() bool
	SetStreamSsrcs(selfSsrcs, peerSsrcs []uint32)
}

type Pipeline struct {
	log   *slog.Logger
	relay Relay

	mu        sync.Mutex
	rtp       *media.RtpSession
	srtp      *media.SrtpSession
	srtcp     *media.SrtcpContext
	srtcpRx4  *media.SrtcpContext
	srtcpRx10 *media.SrtcpContext
	rtcpVerN  int
	selfSsrc  uint32
	depack    *transport.H264Depacketizer
	frameBuf  []byte
	lastAUAt  time.Time
	pktCount  uint32
	octCount  uint32
	srtcpStop chan struct{}

	diagFrames int
	diagKeys   int
	diagBytes  int
	recvDiag   int
	tccSeq     uint16

	OnFrame func(au []byte)
}

func New(log *slog.Logger, relay Relay) *Pipeline {
	if log == nil {
		log = slog.Default()
	}
	return &Pipeline{log: log, relay: relay}
}

func (p *Pipeline) Setup(callID, ourDeviceJid, peerDeviceJid string, sendKM, recvKM core.SrtpKeyingMaterial) error {
	srtp, err := media.NewSrtpSession(sendKM, recvKM, core.SRTPSendAuthTagLen, core.SRTPRecvAuthTagLen)
	if err != nil {
		return err
	}
	selfSsrc := media.GenerateSecureSsrc(callID, ourDeviceJid, slotWord)

	selfSsrcs := make([]uint32, len(callSlots))
	peerSsrcs := make([]uint32, len(callSlots))
	for i, slot := range callSlots {
		selfSsrcs[i] = media.GenerateSecureSsrc(callID, ourDeviceJid, slot)
		peerSsrcs[i] = media.GenerateSecureSsrc(callID, peerDeviceJid, slot)
	}

	// SRTCP usa auth tag de 10 bytes (80 bits) — diferente do SRTP (4). Confirmado
	// autenticando o SR do próprio WhatsApp. Com tag errado o peer rejeita o SR.
	srtcp, err := media.NewSrtcpContext(sendKM, 10)
	if err != nil {
		return err
	}
	rx4, _ := media.NewSrtcpContext(recvKM, 4)
	rx10, _ := media.NewSrtcpContext(recvKM, 10)

	p.mu.Lock()
	p.srtcpRx4 = rx4
	p.srtcpRx10 = rx10
	if p.srtcpStop != nil {
		close(p.srtcpStop)
	}
	p.srtp = srtp
	p.srtcp = srtcp
	p.selfSsrc = selfSsrc
	p.rtp = media.NewH264Session(selfSsrc)
	if p.depack == nil {
		p.depack = &transport.H264Depacketizer{}
	}
	stop := make(chan struct{})
	p.srtcpStop = stop
	p.mu.Unlock()

	p.relay.SetStreamSsrcs(selfSsrcs, peerSsrcs)
	p.log.Debug("video media set up", "self_video_ssrc", selfSsrc,
		"stream_ssrcs", len(selfSsrcs)+len(peerSsrcs))
	go p.senderReportLoop(stop)
	return nil
}

// senderReportLoop envia um RTCP Sender Report (SRTCP) do nosso vídeo a cada 1s.
// Sem o SR o renderizador de vídeo do peer não exibe nosso stream.
func (p *Pipeline) senderReportLoop(stop chan struct{}) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			p.sendSenderReport()
		}
	}
}

func (p *Pipeline) sendSenderReport() {
	p.mu.Lock()
	rtp, srtcp := p.rtp, p.srtcp
	pkts, octs := p.pktCount, p.octCount
	p.mu.Unlock()
	// Só emite SR depois de termos enviado vídeo de fato — evita mandar sender report
	// de vídeo (com 0 pacotes) numa chamada que é só de áudio (o vídeo é keyado em toda
	// call por causa do upgrade mid-call, mas não deve sinalizar stream sem mídia).
	if rtp == nil || srtcp == nil || !p.relay.HasConnection() || pkts == 0 {
		return
	}
	// NTP timestamp (segundos desde 1900) a partir do relógio atual.
	now := time.Now()
	const ntpEpochOffset = 2208988800 // segundos entre 1900 e 1970
	ntpSec := uint32(now.Unix() + ntpEpochOffset)
	ntpFrac := uint32((uint64(now.Nanosecond()) << 32) / 1_000_000_000)

	ssrc := rtp.SSRC()
	sr := media.BuildSenderReport(ssrc, ntpSec, ntpFrac, rtp.Timestamp(), pkts, octs)
	// Compound: SR + SDES (CNAME), igual ao que o WhatsApp manda. O SDES liga o
	// nosso SSRC de vídeo a uma fonte canônica — pode ser o que falta pro peer
	// associar/renderizar o stream.
	cname := fmt.Sprintf("wacalls-%08x", ssrc)
	compound := append(sr, media.BuildSDES(ssrc, cname)...)
	protected := srtcp.Protect(compound)
	if protected != nil {
		p.relay.Broadcast(protected)
	}
}

// buildVideoRtpExt monta o bloco de extensão one-byte (0xBEDE) do vídeo do
// WhatsApp: id3=abs-send-time (3B) + id6=transport-cc seq (2B), padded a 8 bytes.
func buildVideoRtpExt(tccSeq uint16) []byte {
	// abs-send-time: 24 bits, ponto fixo 6.18 dos segundos do relógio atual.
	now := time.Now()
	secs := float64(now.UnixNano()) / 1e9
	abs := uint32(secs*262144.0) & 0xFFFFFF
	ext := make([]byte, 8)
	ext[0] = 0x32 // id=3, len=3
	ext[1] = byte(abs >> 16)
	ext[2] = byte(abs >> 8)
	ext[3] = byte(abs)
	ext[4] = 0x61 // id=6, len=2
	ext[5] = byte(tccSeq >> 8)
	ext[6] = byte(tccSeq)
	ext[7] = 0x00 // padding
	return ext
}

// VideoSSRC devolve o SSRC do nosso stream de vídeo (0 se não configurado).
func (p *Pipeline) VideoSSRC() uint32 {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.selfSsrc
}

func (p *Pipeline) FeedCaptured(au []byte) {
	p.mu.Lock()
	rtp, srtp := p.rtp, p.srtp
	p.mu.Unlock()
	if rtp == nil || srtp == nil || !p.relay.HasConnection() || len(au) == 0 {
		return
	}
	nalus := transport.SplitAnnexB(au)
	if len(nalus) == 0 {
		return
	}
	// DIAG: contar keyframe (tem IDR tipo 5 ou SPS tipo 7) vs delta (só tipo 1) e bitrate.
	isKey := false
	for _, n := range nalus {
		if len(n) > 0 && (n[0]&0x1f == 5 || n[0]&0x1f == 7) {
			isKey = true
			break
		}
	}
	p.mu.Lock()
	p.diagFrames++
	if isKey {
		p.diagKeys++
	}
	p.diagBytes += len(au)
	df, dk, db := p.diagFrames, p.diagKeys, p.diagBytes
	p.mu.Unlock()
	if df%75 == 0 {
		p.log.Info("DIAG video out stats", "frames", df, "keyframes", dk,
			"pct_key", dk*100/df, "avg_au_bytes", db/df, "approx_kbps", db*8/1000/(df/15+1))
	}
	if p.relay.BufferedAmount() > congestionDropBytes {
		return
	}
	var payloads [][]byte
	for _, nalu := range nalus {
		// O WhatsApp real nunca envia AUD (Access Unit Delimiter, tipo 9); o
		// encoder WebCodecs do navegador prefixa um por frame e o decoder do
		// WhatsApp descarta o access unit por causa dele. Remover espelha o peer.
		if len(nalu) > 0 && nalu[0]&0x1f == 9 {
			continue
		}
		payloads = append(payloads, transport.PackageH264NALU(nalu)...)
	}

	p.mu.Lock()
	first := p.lastAUAt.IsZero()
	p.lastAUAt = time.Now()
	p.mu.Unlock()
	if !first {
		rtp.AdvanceTimestamp(rtpStepSamples)
	}
	var sentPkts, sentOcts uint32
	for i, payload := range payloads {
		last := i == len(payloads)-1
		pkt := rtp.CreatePacketWithDuration(payload, 0, last)
		// RTP header extension 0xBEDE (RFC 5285), igual ao vídeo do WhatsApp:
		// id3 = abs-send-time (3B) + id6 = transport-cc seq (2B). Sem isso o relay
		// não processa o nosso vídeo (o áudio é tolerado sem extensão).
		p.mu.Lock()
		p.tccSeq++
		seq := p.tccSeq
		p.mu.Unlock()
		pkt.Header.Extension = true
		pkt.Header.ExtensionProfile = 0xBEDE
		pkt.Header.ExtensionData = buildVideoRtpExt(seq)
		protected, err := srtp.Protect(pkt)
		if err != nil {
			continue
		}
		if df%150 == 0 && i == 0 && len(protected) >= 2 {
			p.log.Info("DIAG OUT rtp header", "b0", fmt.Sprintf("0x%02x", protected[0]),
				"b1", fmt.Sprintf("0x%02x", protected[1]), "X_ext", protected[0]&0x10 != 0)
		}
		p.relay.Broadcast(protected)
		sentPkts++
		sentOcts += uint32(len(payload))
	}
	p.mu.Lock()
	p.pktCount += sentPkts
	p.octCount += sentOcts
	p.mu.Unlock()
}

// VerifyPeerRTCP testa se conseguimos autenticar o SRTCP do peer com a nossa
// derivação SRTCP (chaves de recepção). Se passar, nosso formato bate com o do
// WhatsApp. Só diagnóstico.
func (p *Pipeline) VerifyPeerRTCP(data []byte) {
	p.mu.Lock()
	rx4, rx10 := p.srtcpRx4, p.srtcpRx10
	p.rtcpVerN++
	n := p.rtcpVerN
	p.mu.Unlock()
	if rx4 == nil || n%5 != 1 {
		return
	}
	ok10 := rx10.VerifyAuth(data)
	pt := byte(0)
	if len(data) > 1 {
		pt = data[1]
	}
	// Descriptografa pra validar o IV: num SR (PT 200), os bytes 8-11 são o NTP
	// (segundos desde 1900) — hoje ~0xEDxxxxxx. Se sair coerente, nosso IV está ok.
	rtcp, idx, dok := rx10.Unprotect(data)
	ntpSec := uint32(0)
	if dok && len(rtcp) >= 12 {
		ntpSec = uint32(rtcp[8])<<24 | uint32(rtcp[9])<<16 | uint32(rtcp[10])<<8 | uint32(rtcp[11])
	}
	p.log.Info("DIAG verify peer SRTCP", "pt", pt, "auth_ok_tag10", ok10,
		"decrypt_ok", dok, "index", idx, "ntp_sec_hex", ntpSec)
}

func (p *Pipeline) HandleRelayData(data []byte) {
	p.mu.Lock()
	srtp, depack, selfSsrc := p.srtp, p.depack, p.selfSsrc
	p.mu.Unlock()
	if srtp == nil || depack == nil {
		return
	}
	if media.RTPSsrc(data) == selfSsrc {
		return
	}
	p.mu.Lock()
	p.recvDiag++
	rd := p.recvDiag
	p.mu.Unlock()
	if rd%150 == 1 && len(data) >= 2 {
		// Dump dos bytes do header+extensão (não cifrados no SRTP) pra decodificar
		// a RTP header extension do vídeo do WhatsApp e replicar no nosso envio.
		n := len(data)
		if n > 28 {
			n = 28
		}
		hx := make([]string, n)
		for i := 0; i < n; i++ {
			hx[i] = fmt.Sprintf("%02x", data[i])
		}
		p.log.Info("DIAG IN rtp header (peer video)", "X_ext", data[0]&0x10 != 0,
			"CC", data[0]&0x0f, "bytes", hx)
	}

	pkt, err := srtp.Unprotect(data)
	if err != nil {
		p.log.Debug("video srtp unprotect error", "err", err)
		return
	}
	if len(pkt.Payload) == 0 {
		return
	}
	nalus := depack.Depacketize(pkt.Payload)

	p.mu.Lock()
	for _, nalu := range nalus {
		p.frameBuf = append(p.frameBuf, annexBStartCode...)
		p.frameBuf = append(p.frameBuf, nalu...)
	}
	var frame []byte
	if pkt.Header.Marker && len(p.frameBuf) > 0 {
		frame = p.frameBuf
		p.frameBuf = nil
	}
	cb := p.OnFrame
	p.mu.Unlock()

	if frame != nil && cb != nil {
		cb(frame)
	}
}

func (p *Pipeline) Reset() {
	p.mu.Lock()
	if p.srtcpStop != nil {
		close(p.srtcpStop)
		p.srtcpStop = nil
	}
	p.rtp = nil
	p.srtp = nil
	p.srtcp = nil
	p.selfSsrc = 0
	p.depack = nil
	p.frameBuf = nil
	p.lastAUAt = time.Time{}
	p.pktCount = 0
	p.octCount = 0
	p.mu.Unlock()
}

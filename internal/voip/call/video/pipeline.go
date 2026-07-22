package video

import (
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
	callSlots       = []uint32{0, 1, 4, 2, 3, 5}
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

	mu       sync.Mutex
	rtp      *media.RtpSession
	srtp     *media.SrtpSession
	selfSsrc uint32
	depack   *transport.H264Depacketizer
	frameBuf []byte
	lastAUAt time.Time
	tccSeq   uint16 // sequência transport-cc da extensão RTP de vídeo

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

	p.mu.Lock()
	p.srtp = srtp
	p.selfSsrc = selfSsrc
	p.rtp = media.NewH264Session(selfSsrc)
	if p.depack == nil {
		p.depack = &transport.H264Depacketizer{}
	}
	p.mu.Unlock()

	p.relay.SetStreamSsrcs(selfSsrcs, peerSsrcs)
	p.log.Debug("video media set up", "self_video_ssrc", selfSsrc,
		"stream_ssrcs", len(selfSsrcs)+len(peerSsrcs))
	return nil
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
	if p.relay.BufferedAmount() > congestionDropBytes {
		return
	}
	var payloads [][]byte
	for _, nalu := range nalus {
		// O WhatsApp real nunca envia AUD (Access Unit Delimiter, tipo 9); o encoder
		// WebCodecs do navegador prefixa um por frame e o decoder do WhatsApp descarta
		// o access unit por causa dele. Remover espelha o peer.
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
	for i, payload := range payloads {
		last := i == len(payloads)-1
		pkt := rtp.CreatePacketWithDuration(payload, 0, last)
		// Extensão RTP 0xBEDE (RFC 5285), igual ao vídeo do WhatsApp: id3 = abs-send-time
		// (3B) + id6 = transport-cc seq (2B). SEM isso o relay NÃO processa o nosso vídeo
		// (o áudio é tolerado sem extensão) — o destino não recebe a imagem da câmera.
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
		p.relay.Broadcast(protected)
	}
}

// buildVideoRtpExt monta o bloco de extensão one-byte (0xBEDE) do vídeo do WhatsApp:
// id3 = abs-send-time (3 bytes, ponto fixo 6.18) + id6 = transport-cc seq (2 bytes).
func buildVideoRtpExt(tccSeq uint16) []byte {
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
	p.rtp = nil
	p.srtp = nil
	p.selfSsrc = 0
	p.depack = nil
	p.frameBuf = nil
	p.lastAUAt = time.Time{}
	p.mu.Unlock()
}

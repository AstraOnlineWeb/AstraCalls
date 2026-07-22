package video

import (
	"encoding/binary"
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

	// Estado da extensão de vídeo do WhatsApp (perfil 0xDEBE) no envio.
	frameNumber      uint16 // nº do frame (por access unit), começa em 1
	transportSeq     uint16 // sequência transport (por pacote)
	keyframeRequired bool   // o primeiro frame enviado precisa ser IDR

	// Pedido de keyframe (RTCP PLI) ao peer para o painel decodificar sem esperar
	// o keyframe periódico (~25s).
	srtcpSend     *media.SrtcpContext
	peerVideoSsrc uint32
	pliSent       bool

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
	peerVideoSsrc := media.GenerateSecureSsrc(callID, peerDeviceJid, slotWord)
	srtcpSend, _ := media.NewSrtcpContext(sendKM, 10) // RTCP usa auth tag de 10 bytes

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
	p.srtcpSend = srtcpSend
	p.peerVideoSsrc = peerVideoSsrc
	p.pliSent = false
	p.frameNumber = 1
	p.transportSeq = 0
	p.keyframeRequired = true
	p.lastAUAt = time.Time{}
	p.mu.Unlock()

	p.relay.SetStreamSsrcs(selfSsrcs, peerSsrcs)
	p.log.Debug("video media set up", "self_video_ssrc", selfSsrc,
		"stream_ssrcs", len(selfSsrcs)+len(peerSsrcs))
	return nil
}

// videoExtProfile é o perfil da RTP header extension do vídeo do WhatsApp (0xDEBE,
// NÃO 0xBEDE). O conteúdo (one-byte headers) traz MediaFrameInfo, InitialBandwidth,
// ShortOffset e TransportSequence — não abs-send-time. Formato confirmado contra o
// vídeo real do WhatsApp e o meowcaller/whatsapp-rust.
const videoExtProfile = 0xDEBE

const (
	videoMediaFrameIDR   = 0x08 // frame é IDR/keyframe
	videoMediaFrameDelta = 0x20 // frame delta
)

// buildWhatsappVideoExt monta o bloco da extensão 0xDEBE (padded a múltiplo de 4).
// frameNumber só entra no PRIMEIRO pacote de cada access unit (ptr não-nil).
func buildWhatsappVideoExt(mediaFrameInfo uint8, frameNumber *uint16, transportSeq uint16) []byte {
	ext := make([]byte, 0, 16)
	if frameNumber != nil {
		ext = append(ext, 0x32, mediaFrameInfo, byte(*frameNumber>>8), byte(*frameNumber)) // id3, len3
	} else {
		ext = append(ext, 0x30, mediaFrameInfo) // id3, len1
	}
	ext = append(ext, 0x51, 0x00, 0x00)                                     // id5 InitialBandwidth=0
	ext = append(ext, 0x61, 0x00, 0x00)                                     // id6 ShortOffset=0
	ext = append(ext, 0x91, byte(transportSeq>>8), byte(transportSeq))      // id9 TransportSequence
	for len(ext)%4 != 0 {
		ext = append(ext, 0x00) // padding até fronteira de 4 bytes
	}
	return ext
}

func (p *Pipeline) FeedCaptured(au []byte) {
	p.mu.Lock()
	rtp, srtp := p.rtp, p.srtp
	keyframeRequired := p.keyframeRequired
	frameNum := p.frameNumber
	tseq := p.transportSeq
	first := p.lastAUAt.IsZero()
	p.mu.Unlock()
	if rtp == nil || srtp == nil || !p.relay.HasConnection() || len(au) == 0 {
		return
	}
	nalus := transport.SplitAnnexB(au)
	if len(nalus) == 0 {
		return
	}
	idr := false
	for _, n := range nalus {
		if len(n) > 0 && n[0]&0x1f == 5 { // NAL tipo 5 = IDR
			idr = true
			break
		}
	}
	// O primeiro frame enviado precisa ser keyframe, senão o peer não decodifica.
	if keyframeRequired && !idr {
		return
	}
	// Junta TODOS os NALUs (menos AUD tipo 9) num único access unit Annex-B e fragmenta
	// como UM NAL — é assim que o WhatsApp manda o vídeo (não NALU a NALU).
	var packed []byte
	for _, n := range nalus {
		if len(n) == 0 || n[0]&0x1f == 9 {
			continue
		}
		if len(packed) > 0 {
			packed = append(packed, 0, 0, 0, 1)
		}
		packed = append(packed, n...)
	}
	if len(packed) == 0 {
		return
	}
	if p.relay.BufferedAmount() > congestionDropBytes {
		return
	}
	if !first {
		rtp.AdvanceTimestamp(rtpStepSamples)
	}
	mediaFrameInfo := uint8(videoMediaFrameDelta)
	if idr {
		mediaFrameInfo = videoMediaFrameIDR
	}
	payloads := transport.PackageH264NALU(packed)
	for i, payload := range payloads {
		last := i == len(payloads)-1
		var fn *uint16
		if i == 0 {
			v := frameNum
			fn = &v // FrameNumber só no primeiro pacote do access unit
		}
		pkt := rtp.CreatePacketWithDuration(payload, 0, last)
		pkt.Header.Extension = true
		pkt.Header.ExtensionProfile = videoExtProfile
		pkt.Header.ExtensionData = buildWhatsappVideoExt(mediaFrameInfo, fn, tseq)
		tseq++
		if protected, err := srtp.Protect(pkt); err == nil {
			p.relay.Broadcast(protected)
		}
	}

	p.mu.Lock()
	p.lastAUAt = time.Now()
	p.transportSeq = tseq
	p.frameNumber = frameNum + 1
	if idr {
		p.keyframeRequired = false
	}
	p.mu.Unlock()
}

// requestKeyframeBurst pede um keyframe ao peer (RTCP PLI) algumas vezes nos
// primeiros segundos, para o painel decodificar sem esperar o keyframe periódico
// do WhatsApp (que pode levar ~25s).
func (p *Pipeline) requestKeyframeBurst() {
	for i := 0; i < 4; i++ {
		p.sendKeyframeRequest()
		time.Sleep(1500 * time.Millisecond)
	}
}

// sendKeyframeRequest envia um RTCP PSFB PLI (PT 206, FMT 1) para o SSRC de vídeo
// do peer, protegido por SRTCP.
func (p *Pipeline) sendKeyframeRequest() {
	p.mu.Lock()
	srtcp, selfSsrc, peerSsrc := p.srtcpSend, p.selfSsrc, p.peerVideoSsrc
	p.mu.Unlock()
	if srtcp == nil || peerSsrc == 0 || !p.relay.HasConnection() {
		return
	}
	pli := make([]byte, 12)
	pli[0] = 0x81 // V=2, P=0, FMT=1 (PLI)
	pli[1] = 206  // PSFB
	pli[2] = 0x00
	pli[3] = 0x02 // length = 2 (3 palavras de 32 bits - 1)
	binary.BigEndian.PutUint32(pli[4:8], selfSsrc)
	binary.BigEndian.PutUint32(pli[8:12], peerSsrc)
	if protected := srtcp.Protect(pli); protected != nil {
		p.relay.Broadcast(protected)
	}
}

func (p *Pipeline) HandleRelayData(data []byte) {
	p.mu.Lock()
	srtp, depack, selfSsrc := p.srtp, p.depack, p.selfSsrc
	// Primeiro pacote de vídeo do peer -> pede keyframe (PLI) para o painel iniciar rápido.
	needPli := !p.pliSent && p.srtcpSend != nil && p.peerVideoSsrc != 0
	if needPli {
		p.pliSent = true
	}
	p.mu.Unlock()
	if needPli {
		go p.requestKeyframeBurst()
	}
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
	p.frameNumber = 1
	p.transportSeq = 0
	p.keyframeRequired = true
	p.srtcpSend = nil
	p.peerVideoSsrc = 0
	p.pliSent = false
	p.mu.Unlock()
}

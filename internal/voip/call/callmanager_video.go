package call

import (
	"time"
	"wacalls/internal/voip/core"
	"wacalls/internal/voip/media"
	"wacalls/internal/voip/transport"
)

const (
	videoRtpStepSamples      = 90000 / 15
	videoCongestionDropBytes = 48 * 1024
	videoSlotWord            = 2
)

var (
	videoCallSlots  = []uint32{0, 1, 4, 2, 3, 5}
	annexBStartCode = []byte{0, 0, 0, 1}
)

func (m *CallManager) setupVideoMediaLocked(sendKM, recvKM core.SrtpKeyingMaterial, ourDeviceJid, peerDeviceJid string) {
	call := m.currentCall
	if call == nil || call.MediaType != core.CallMediaTypeVideo {
		return
	}
	vsess, err := media.NewSrtpSession(sendKM, recvKM, core.SRTPSendAuthTagLen, core.SRTPRecvAuthTagLen)
	if err != nil {
		m.log.Error("video srtp session failed", "err", err)
		return
	}
	m.videoSrtpSession = vsess
	m.videoSelfSsrc = media.GenerateSecureSsrc(call.CallID, ourDeviceJid, videoSlotWord)
	m.videoRtpSession = media.NewH264Session(m.videoSelfSsrc)
	if m.videoDepacketizer == nil {
		m.videoDepacketizer = &transport.H264Depacketizer{}
	}

	selfSsrcs := make([]uint32, len(videoCallSlots))
	peerSsrcs := make([]uint32, len(videoCallSlots))
	for i, slot := range videoCallSlots {
		selfSsrcs[i] = media.GenerateSecureSsrc(call.CallID, ourDeviceJid, slot)
		peerSsrcs[i] = media.GenerateSecureSsrc(call.CallID, peerDeviceJid, slot)
	}
	m.relay.SetStreamSsrcs(selfSsrcs, peerSsrcs)
	m.log.Debug("video media set up", "self_video_ssrc", m.videoSelfSsrc,
		"stream_ssrcs", len(selfSsrcs)+len(peerSsrcs))
}

func (m *CallManager) FeedCapturedVideo(au []byte) {
	m.mu.Lock()
	rtpSess, srtpSess, relay := m.videoRtpSession, m.videoSrtpSession, m.relay
	m.mu.Unlock()
	if rtpSess == nil || srtpSess == nil || !relay.HasConnection() || len(au) == 0 {
		return
	}
	nalus := transport.SplitAnnexB(au)
	if len(nalus) == 0 {
		return
	}
	if relay.BufferedAmount() > videoCongestionDropBytes {
		return
	}
	var payloads [][]byte
	for _, nalu := range nalus {
		payloads = append(payloads, transport.PackageH264NALU(nalu)...)
	}

	m.mu.Lock()
	first := m.lastVideoAUAt.IsZero()
	m.lastVideoAUAt = time.Now()
	m.mu.Unlock()
	if !first {
		rtpSess.AdvanceTimestamp(videoRtpStepSamples)
	}
	for i, p := range payloads {
		last := i == len(payloads)-1
		pkt := rtpSess.CreatePacketWithDuration(p, 0, last)
		srtp, err := srtpSess.Protect(pkt)
		if err != nil {
			continue
		}
		relay.Broadcast(srtp)
	}
}

func (m *CallManager) handleVideoRelayData(data []byte) {
	m.mu.Lock()
	if m.videoSrtpSession == nil || m.videoDepacketizer == nil {
		m.mu.Unlock()
		return
	}
	if readRtpSsrc(data) == m.videoSelfSsrc {
		m.mu.Unlock()
		return
	}
	srtp := m.videoSrtpSession
	depack := m.videoDepacketizer
	m.mu.Unlock()

	pkt, err := srtp.Unprotect(data)
	if err != nil {
		m.log.Debug("video srtp unprotect error", "err", err)
		return
	}
	if len(pkt.Payload) == 0 {
		return
	}
	nalus := depack.Depacketize(pkt.Payload)

	m.mu.Lock()
	for _, nalu := range nalus {
		m.videoFrameBuf = append(m.videoFrameBuf, annexBStartCode...)
		m.videoFrameBuf = append(m.videoFrameBuf, nalu...)
	}
	var frame []byte
	if pkt.Header.Marker && len(m.videoFrameBuf) > 0 {
		frame = m.videoFrameBuf
		m.videoFrameBuf = nil
	}
	cb := m.OnPeerVideo
	m.mu.Unlock()

	if frame != nil && cb != nil {
		cb(frame)
	}
}

func readRtpSsrc(data []byte) uint32 {
	return uint32(data[8])<<24 | uint32(data[9])<<16 | uint32(data[10])<<8 | uint32(data[11])
}

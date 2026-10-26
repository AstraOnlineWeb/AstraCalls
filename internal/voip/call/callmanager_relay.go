package call

import (
	"math"
	"sync/atomic"

	"wacalls/internal/voip/core"
	"wacalls/internal/voip/transport"
)

type RelayTransport interface {
	SetSsrc(ssrc uint32)
	SetSubscriptionSsrc(ssrc uint32)
	SetStreamSsrcs(selfSsrcs, peerSsrcs []uint32)
	SetGroupAllocate(fn func(ip string, port int, rawToken, key []byte) []byte)
	SetOnConnected(fn func(ip string, port int))
	SetOnReceive(fn func(data []byte))
	ResendSubscriptions()
	ConfigureRelays(relays []transport.RelayConfig)
	Broadcast(data []byte)
	BufferedAmount() uint64
	HasConnection() bool
	ConnectedCount() int
	Cleanup()
}

var _ RelayTransport = (*transport.SctpRelayManager)(nil)

func (m *CallManager) onRelayConnected() {
	m.mu.Lock()
	call := m.currentCall
	if call != nil && call.StateData.State == core.CallStateConnecting {
		if err := call.ApplyTransition(Transition{Type: TransitionMediaConnected}); err == nil {
			m.emitState()
			m.startMediaSendLoopLocked()
			m.log.Info("relay connected → active", "call_id", call.CallID)
		}
	}
	m.mu.Unlock()
}

func buildRelayConfigs(endpoints []core.RelayEndpoint) []transport.RelayConfig {
	seen := map[string]bool{}
	var relays []transport.RelayConfig
	for _, ep := range endpoints {
		if ep.Protocol != 0 {
			continue
		}
		if ep.Key == "" || ep.RawToken == nil {
			continue
		}
		key := ep.IP
		if seen[key] {
			continue
		}
		seen[key] = true
		name := ep.RelayName
		if name == "" {
			name = ep.IP
		}
		relays = append(relays, transport.RelayConfig{
			IP: ep.IP, Port: 3478, Token: ep.Token, AuthToken: ep.AuthToken,
			RawAuthToken: ep.RawAuthToken, RawToken: ep.RawToken, Key: ep.Key,
			RelayID: ep.RelayID, Name: name, AuthTokenID: ep.AuthTokenID,
		})
	}
	return relays
}

func (m *CallManager) connectRelays(endpoints []core.RelayEndpoint) {
	relays := buildRelayConfigs(endpoints)
	if len(relays) == 0 {
		m.log.Error("no usable relay configs")
		return
	}
	m.mu.Lock()
	m.relay.SetSsrc(m.selfSsrc)
	m.relay.SetSubscriptionSsrc(firstSsrc(m.peerSsrcs))
	m.mu.Unlock()
	m.relay.ConfigureRelays(relays)
	m.log.Info("relay configured", "connected", m.relay.ConnectedCount())
}

func (m *CallManager) cleanupMedia() {
	m.mu.Lock()
	m.logMediaStatsLocked()
	codec := m.codec
	m.codec = nil
	if m.sendLoopStop != nil {
		close(m.sendLoopStop)
		m.sendLoopStop = nil
	}
	m.rtpSession = nil
	m.srtpSession = nil
	m.peerCallKey = nil
	m.mediaClock = nil
	m.firstPacketSent = false
	m.initialTransportSent = false
	m.outgoingPreacceptSent = false
	m.actualPeerSet = false
	m.captureBuf = nil
	m.audioTimelineSet = false
	m.audioPlayedSamples = 0
	m.mu.Unlock()

	m.video.Reset()
	m.relay.Cleanup()
	if codec != nil {
		codec.Close()
	}
}

// logMediaStatsLocked registra (INFO) o resumo da mídia de ENTRADA da chamada que
// termina e zera os contadores. Explica "não ouço o cliente": rtp=0 → relay não
// entrega (assinatura de SSRC); decoded>0 com rms≈0 → chave SRTP errada ou peer mudo.
func (m *CallManager) logMediaStatsLocked() {
	rtp := atomic.SwapUint64(&m.rtpRecvN, 0)
	audio := atomic.SwapUint64(&m.audioRtpN, 0)
	unprot := atomic.SwapUint64(&m.unprotectErrN, 0)
	decoded := atomic.SwapUint64(&m.recvDiagN, 0)
	rms := 0.0
	if m.peerEnergySamples > 0 {
		rms = math.Sqrt(m.peerEnergySum / float64(m.peerEnergySamples))
	}
	m.peerEnergySum, m.peerEnergySamples = 0, 0
	if m.currentCall == nil && rtp == 0 && decoded == 0 {
		return
	}
	callID := ""
	if m.currentCall != nil {
		callID = m.currentCall.CallID
	}
	m.log.Info("call media stats (entrada)", "call_id", callID, "rtp_pkts", rtp, "audio_pkts", audio,
		"srtp_errors", unprot, "decoded_frames", decoded, "peer_rms", math.Round(rms*10000)/10000,
		"accepted_by", m.acceptedByJid)
}

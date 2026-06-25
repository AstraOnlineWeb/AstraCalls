package call

import (
	"context"
	"log/slog"
	"sync"
	"time"
	"wacalls/internal/voip/core"
	"wacalls/internal/voip/media"
	"wacalls/internal/voip/signaling"
	"wacalls/internal/voip/transport"
	"wacalls/internal/voip/wanode"

	"go.mau.fi/whatsmeow/types"
)

type CallManager struct {
	sock core.VoipSocket
	log  *slog.Logger

	mu          sync.Mutex
	currentCall *CallInfo

	rtpSession  *media.RtpSession
	srtpSession *media.SrtpSession
	codec       media.Codec
	relay       RelayTransport

	selfSsrc      uint32
	peerSsrcs     []uint32
	actualPeerSet bool

	videoRtpSession   *media.RtpSession
	videoSrtpSession  *media.SrtpSession
	videoSelfSsrc     uint32
	videoDepacketizer *transport.H264Depacketizer
	videoFrameBuf     []byte
	lastVideoAUAt     time.Time

	firstPacketSent       bool
	initialTransportSent  bool
	outgoingPreacceptSent bool
	acceptedByJid         string
	debeEnabled           bool

	captureBuf   []float32
	sendLoopStop chan struct{}

	audioTimelineSet   bool
	audioBaseTs        uint32
	audioPlayedSamples uint64

	OnStateChange func(*CallInfo)
	OnIncoming    func(*CallInfo)
	OnEnded       func(*CallInfo)
	OnPeerAudio   func([]float32)
	OnPeerVideo   func([]byte)
}

func NewCallManager(sock core.VoipSocket, log *slog.Logger) *CallManager {
	if log == nil {
		log = slog.Default()
	}
	m := &CallManager{
		sock:        sock,
		log:         log,
		debeEnabled: true,
	}
	relay := transport.NewSctpRelayManager(log)
	relay.SetOnConnected(func(ip string, port int) { m.onRelayConnected() })
	relay.SetOnReceive(func(data []byte) { m.onRelayData(data) })
	m.relay = relay
	return m
}

func (m *CallManager) CurrentCall() *CallInfo {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.currentCall
}

func (m *CallManager) emitState() {
	if m.OnStateChange != nil && m.currentCall != nil {
		m.OnStateChange(m.currentCall)
	}
}

func (m *CallManager) StartCall(ctx context.Context, callID string, peerJid types.JID, isVideo bool) error {
	m.mu.Lock()
	if m.currentCall != nil && !m.currentCall.IsEnded() {
		m.mu.Unlock()
		return &CallError{"a call is already in progress"}
	}

	mediaType := core.CallMediaTypeAudio
	if isVideo {
		mediaType = core.CallMediaTypeVideo
	}
	creator := m.sock.OwnLID()
	if creator.IsEmpty() {
		creator = m.sock.OwnPN()
	}
	resolved := m.sock.ResolveLIDForPN(ctx, peerJid)

	call := NewOutgoingCall(callID, resolved.String(), creator.String(), mediaType)
	callKey := media.GenerateCallKey()
	call.EncryptionKey = callKey
	m.currentCall = call
	m.initialTransportSent = false
	m.outgoingPreacceptSent = false

	selfJid := creator.String()
	m.selfSsrc = media.GenerateSecureSsrc(callID, selfJid, 0)
	m.rtpSession = media.NewWhatsAppOpusSession(m.selfSsrc)
	m.peerSsrcs = []uint32{media.GenerateSecureSsrc(callID, resolved.String(), 0)}
	m.initCodec()
	m.mu.Unlock()

	offer, err := signaling.BuildOfferStanza(ctx, m.sock, callID, callKey, resolved, isVideo)
	if err != nil {
		return err
	}

	m.mu.Lock()
	_ = m.currentCall.ApplyTransition(Transition{Type: TransitionOfferSent})
	m.emitState()
	m.mu.Unlock()

	// Envia o offer e trata o ack em background: o aparelho toca quando recebe o
	// offer, não quando o Query retorna. Antes travava até 15s no timeout do Query.
	go func() {
		ackNode, qerr := m.sock.Query(context.Background(), offer)
		if qerr != nil {
			m.log.Error("offer query error", "err", qerr)
			return
		}
		if ackNode != nil {
			m.log.Info("offer ack", "xml", ackNode.String())
			m.HandleCallAck(context.Background(), ackNode)
		}
	}()

	m.log.Info("call offer sent", "call_id", callID, "peer", resolved.String())
	return nil
}

func (m *CallManager) AcceptCall(ctx context.Context, callID string) error {
	m.mu.Lock()
	call := m.currentCall
	if call == nil || call.CallID != callID {
		m.mu.Unlock()
		return &CallError{"no incoming call with id " + callID}
	}
	if !call.CanAccept() {
		m.mu.Unlock()
		return &CallError{"call cannot be accepted in state " + string(call.StateData.State)}
	}
	_ = call.ApplyTransition(Transition{Type: TransitionLocalAccepted})
	m.emitState()
	key := call.EncryptionKey
	peer := wanode.MustJID(call.PeerJid)
	creator := wanode.MustJID(call.CallCreator)
	isVideo := call.MediaType == core.CallMediaTypeVideo
	relayData := call.RelayData
	m.mu.Unlock()

	if key != nil {
		acceptNode, err := signaling.BuildAcceptStanza(ctx, m.sock, callID, key, peer, creator, isVideo)
		if err != nil {
			m.log.Error("build accept failed", "err", err)
		} else {
			// envia o accept em background: não bloquear a UI até 15s no timeout do ack
			go func() {
				if _, err := m.sock.Query(context.Background(), acceptNode); err != nil {
					m.log.Error("accept query error", "err", err)
				}
			}()
		}
	}

	if relayData != nil {
		m.connectRelays(relayData.Endpoints)
	}
	m.log.Info("call accepted", "call_id", callID)
	return nil
}

func (m *CallManager) RejectCall(ctx context.Context, callID string, reason core.EndCallReason) error {
	m.mu.Lock()
	call := m.currentCall
	if call == nil || call.CallID != callID {
		m.mu.Unlock()
		return &CallError{"no call with id " + callID}
	}
	_ = call.ApplyTransition(Transition{Type: TransitionLocalRejected, Reason: reason})
	node := signaling.BuildRejectStanza(wanode.MustJID(call.PeerJid), call.CallID, wanode.MustJID(call.CallCreator))
	m.emitState()
	m.mu.Unlock()

	go func() { _, _ = m.sock.Query(ctx, node) }()
	m.cleanupMedia()
	return nil
}

func (m *CallManager) EndCall(ctx context.Context, reason core.EndCallReason) error {
	m.mu.Lock()
	call := m.currentCall
	if call == nil || call.IsEnded() {
		m.mu.Unlock()
		return nil
	}
	_ = call.ApplyTransition(Transition{Type: TransitionTerminated, Reason: reason})
	node := signaling.BuildTerminateStanza(wanode.MustJID(call.PeerJid), call.CallID, wanode.MustJID(call.CallCreator))
	ended := call
	m.emitState()
	m.mu.Unlock()

	go func() { _, _ = m.sock.Query(ctx, node) }()
	if m.OnEnded != nil {
		m.OnEnded(ended)
	}
	m.cleanupMedia()
	return nil
}

func (m *CallManager) ownCredJid() string {
	lid := m.sock.OwnLID()
	if !lid.IsEmpty() {
		return lid.String()
	}
	return m.sock.OwnPN().String()
}

type CallError struct{ Msg string }

func (e *CallError) Error() string { return e.Msg }

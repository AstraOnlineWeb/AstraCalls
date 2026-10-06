package call

import (
	"context"

	"wacalls/internal/voip/signaling"
	"wacalls/internal/voip/wanode"

	waBinary "go.mau.fi/whatsmeow/binary"
)

// Ações mid-call portadas do zapo-js (MIT): mute_v2 (microfone) e raise_hand (mão).
// Espelham o padrão do HandleVideoState: ack tipado obrigatório + estado + callback.

// HandleMuteV2 trata um <call><mute_v2 mute-state=0|1/></call> recebido: ack, atualiza
// PeerAudioMuted e dispara OnPeerMute. request-state (mecânica de grupo) é ignorado.
func (m *CallManager) HandleMuteV2(ctx context.Context, node *waBinary.Node) {
	info := signaling.ExtractNodeInfo(node)
	if info == nil || info.Tag != "mute_v2" {
		return
	}
	if ack, ok := signaling.BuildCallAck(node, "mute_v2"); ok {
		_ = m.sock.SendNode(ctx, ack)
	}
	muted, isRequest := signaling.ParseMuteV2(info.InnerNode)
	if isRequest || muted == nil {
		return
	}
	m.mu.Lock()
	call := m.currentCall
	if call == nil || call.CallID != info.CallID || call.StateData.PeerAudioMuted == *muted {
		m.mu.Unlock()
		return
	}
	call.StateData.PeerAudioMuted = *muted
	cb := m.OnPeerMute
	c := call
	m.mu.Unlock()
	m.log.Info("peer mute state", "call_id", info.CallID, "muted", *muted)
	if cb != nil {
		cb(c)
	}
}

// HandleRaiseHand trata um <call><user_action action=raise_hand>…</call> (ou o
// <raise_hand> no topo): ack, atualiza PeerHandRaised e dispara OnHandRaise.
func (m *CallManager) HandleRaiseHand(ctx context.Context, node *waBinary.Node) {
	info := signaling.ExtractNodeInfo(node)
	if info == nil {
		return
	}
	if ack, ok := signaling.BuildCallAck(node, info.Tag); ok {
		_ = m.sock.SendNode(ctx, ack)
	}
	raised := signaling.ParseRaiseHandState(info.InnerNode)
	if raised == nil {
		return
	}
	m.mu.Lock()
	call := m.currentCall
	if call == nil || call.CallID != info.CallID || call.StateData.PeerHandRaised == *raised {
		m.mu.Unlock()
		return
	}
	call.StateData.PeerHandRaised = *raised
	cb := m.OnHandRaise
	c := call
	m.mu.Unlock()
	m.log.Info("peer raise hand", "call_id", info.CallID, "raised", *raised)
	if cb != nil {
		cb(c)
	}
}

// AnnounceMute avisa o peer do nosso estado de microfone (<mute_v2>) e guarda local.
// Fire-and-forget: o mic já está no estado de qualquer forma; um envio falho só loga.
func (m *CallManager) AnnounceMute(ctx context.Context, muted bool) error {
	m.mu.Lock()
	call := m.currentCall
	if call == nil || !call.IsActive() {
		m.mu.Unlock()
		return &CallError{"no active call"}
	}
	call.StateData.AudioMuted = muted
	to := m.videoPeerLocked()
	creator := wanode.MustJID(call.CallCreator)
	callID := call.CallID
	m.mu.Unlock()
	muteState := 0
	if muted {
		muteState = 1
	}
	return m.sock.SendNode(ctx, signaling.BuildMuteV2Stanza(to, callID, creator, muteState))
}

// SetHandRaised anuncia a nossa mão levantada/baixada (idempotente).
func (m *CallManager) SetHandRaised(ctx context.Context, raised bool) error {
	m.mu.Lock()
	call := m.currentCall
	if call == nil || !call.IsActive() {
		m.mu.Unlock()
		return &CallError{"no active call"}
	}
	if call.StateData.HandRaised == raised {
		m.mu.Unlock()
		return nil
	}
	call.StateData.HandRaised = raised
	to := m.videoPeerLocked()
	creator := wanode.MustJID(call.CallCreator)
	callID := call.CallID
	m.mu.Unlock()
	return m.sock.SendNode(ctx, signaling.BuildRaiseHandStanza(to, creator, callID, raised))
}

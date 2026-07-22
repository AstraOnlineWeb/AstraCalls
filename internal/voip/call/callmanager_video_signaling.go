package call

import (
	"context"

	"wacalls/internal/voip/core"
	"wacalls/internal/voip/signaling"
	"wacalls/internal/voip/wanode"

	waBinary "go.mau.fi/whatsmeow/binary"
	"go.mau.fi/whatsmeow/types"
)

// videoPeerLocked resolve o destino das stanzas de vídeo mid-call: o dispositivo
// que aceitou a chamada quando conhecido, senão o JID base do peer. Requer m.mu.
func (m *CallManager) videoPeerLocked() types.JID {
	if m.acceptedByJid != "" {
		return wanode.MustJID(m.acceptedByJid)
	}
	return wanode.MustJID(m.currentCall.PeerJid)
}

// HandleVideoState trata uma stanza <call><video state=N/></call> recebida no meio
// de uma chamada ativa: manda o ack obrigatório, atualiza o estado de vídeo e
// dispara os callbacks para a UI. Quando o peer aceita um upgrade que pedimos,
// confirmamos automaticamente com <video state=1> (câmera ligada).
func (m *CallManager) HandleVideoState(ctx context.Context, node *waBinary.Node) {
	info := signaling.ExtractNodeInfo(node)
	if info == nil || info.Tag != "video" {
		return
	}
	// O ack tipado é obrigatório: sem ele o WhatsApp interrompe a negociação.
	if ack, ok := signaling.BuildVideoAck(node); ok {
		_ = m.sock.SendNode(ctx, ack)
	}
	state := wanode.AttrInt(info.InnerNode.Attrs, "state", -1)

	m.mu.Lock()
	call := m.currentCall
	if call == nil || call.CallID != info.CallID || !call.IsActive() {
		m.mu.Unlock()
		return
	}
	sd := &call.StateData
	var upgradeRequested, sendEnabled bool
	switch state {
	case signaling.VideoStateUpgradeRequest, signaling.VideoStateUpgradeRequestV2:
		sd.VideoUpgradeIncoming = true
		upgradeRequested = true
	case signaling.VideoStateUpgradeAccept:
		// O peer aceitou o upgrade que pedimos: ligamos a câmera e confirmamos.
		sd.VideoUpgradeOutgoing = false
		sd.VideoOff = false
		call.MediaType = core.CallMediaTypeVideo
		sendEnabled = true
	case signaling.VideoStateUpgradeReject, signaling.VideoStateUpgradeCancel:
		sd.VideoUpgradeIncoming = false
		sd.VideoUpgradeOutgoing = false
	case signaling.VideoStateEnabled:
		sd.PeerVideoOn = true
		call.MediaType = core.CallMediaTypeVideo
	case signaling.VideoStateStopped, signaling.VideoStateDisabled:
		sd.PeerVideoOn = false
	}
	peer := m.videoPeerLocked()
	creator := wanode.MustJID(call.CallCreator)
	callID := call.CallID
	onReq := m.OnVideoUpgradeRequest
	onChg := m.OnVideoStateChanged
	c := call
	m.mu.Unlock()

	if sendEnabled {
		orientation := 0
		_ = m.sock.SendNode(ctx, signaling.BuildVideoStateStanza(signaling.VideoStateParams{
			CallID: callID, To: peer, CallCreator: creator,
			State: signaling.VideoStateEnabled, DeviceOrientation: &orientation,
		}))
	}
	if upgradeRequested && onReq != nil {
		onReq(c)
	}
	if onChg != nil {
		onChg(c)
	}
	m.log.Info("peer video state", "call_id", callID, "state", state)
}

// RequestVideoUpgrade pede à outra ponta um upgrade de áudio->vídeo (state=11).
func (m *CallManager) RequestVideoUpgrade(ctx context.Context) error {
	m.mu.Lock()
	call := m.currentCall
	if call == nil || !call.IsActive() {
		m.mu.Unlock()
		return &CallError{"no active call to upgrade"}
	}
	call.StateData.VideoUpgradeOutgoing = true
	peer := m.videoPeerLocked()
	creator := wanode.MustJID(call.CallCreator)
	callID := call.CallID
	m.emitState()
	m.mu.Unlock()

	orientation := 0
	return m.sock.SendNode(ctx, signaling.BuildVideoStateStanza(signaling.VideoStateParams{
		CallID: callID, To: peer, CallCreator: creator,
		State: signaling.VideoStateUpgradeRequestV2, Dec: signaling.VideoDecRequest,
		DeviceOrientation: &orientation,
	}))
}

// AcceptVideoUpgrade aceita um pedido de upgrade recebido: responde state=4 e, em
// seguida, state=1 (câmera ligada).
func (m *CallManager) AcceptVideoUpgrade(ctx context.Context) error {
	m.mu.Lock()
	call := m.currentCall
	if call == nil || !call.IsActive() {
		m.mu.Unlock()
		return &CallError{"no active call to upgrade"}
	}
	call.StateData.VideoUpgradeIncoming = false
	call.StateData.VideoOff = false
	call.MediaType = core.CallMediaTypeVideo
	peer := m.videoPeerLocked()
	creator := wanode.MustJID(call.CallCreator)
	callID := call.CallID
	m.emitState()
	m.mu.Unlock()

	accept := signaling.BuildVideoStateStanza(signaling.VideoStateParams{
		CallID: callID, To: peer, CallCreator: creator,
		State: signaling.VideoStateUpgradeAccept, Dec: signaling.VideoDecAccept,
	})
	if err := m.sock.SendNode(ctx, accept); err != nil {
		return err
	}
	orientation := 0
	return m.sock.SendNode(ctx, signaling.BuildVideoStateStanza(signaling.VideoStateParams{
		CallID: callID, To: peer, CallCreator: creator,
		State: signaling.VideoStateEnabled, DeviceOrientation: &orientation,
	}))
}

// RejectVideoUpgrade recusa um pedido de upgrade recebido (state=5).
func (m *CallManager) RejectVideoUpgrade(ctx context.Context) error {
	m.mu.Lock()
	call := m.currentCall
	if call == nil || !call.IsActive() {
		m.mu.Unlock()
		return &CallError{"no active call"}
	}
	call.StateData.VideoUpgradeIncoming = false
	peer := m.videoPeerLocked()
	creator := wanode.MustJID(call.CallCreator)
	callID := call.CallID
	m.emitState()
	m.mu.Unlock()

	return m.sock.SendNode(ctx, signaling.BuildVideoStateStanza(signaling.VideoStateParams{
		CallID: callID, To: peer, CallCreator: creator,
		State: signaling.VideoStateUpgradeReject,
	}))
}

// StopVideo desliga o nosso vídeo mid-call (downgrade para áudio, state=6).
func (m *CallManager) StopVideo(ctx context.Context) error {
	m.mu.Lock()
	call := m.currentCall
	if call == nil || !call.IsActive() {
		m.mu.Unlock()
		return &CallError{"no active call"}
	}
	call.StateData.VideoOff = true
	call.StateData.VideoUpgradeOutgoing = false
	peer := m.videoPeerLocked()
	creator := wanode.MustJID(call.CallCreator)
	callID := call.CallID
	m.emitState()
	m.mu.Unlock()

	orientation := 0
	return m.sock.SendNode(ctx, signaling.BuildVideoStateStanza(signaling.VideoStateParams{
		CallID: callID, To: peer, CallCreator: creator,
		State: signaling.VideoStateStopped, DeviceOrientation: &orientation,
	}))
}

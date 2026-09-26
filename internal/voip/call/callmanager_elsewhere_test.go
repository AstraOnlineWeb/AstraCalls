package call

import (
	"context"
	"log/slog"
	"testing"

	"wacalls/internal/voip/core"
	"wacalls/internal/voip/transport"
	"wacalls/internal/voip/wanode"

	waBinary "go.mau.fi/whatsmeow/binary"
	"go.mau.fi/whatsmeow/types"
)

// fakeRelay é um RelayTransport de teste sem conexão real.
type fakeRelay struct{}

func (fakeRelay) SetSsrc(uint32)                          {}
func (fakeRelay) SetSubscriptionSsrc(uint32)              {}
func (fakeRelay) SetStreamSsrcs([]uint32, []uint32)       {}
func (fakeRelay) SetOnConnected(func(string, int))        {}
func (fakeRelay) SetOnReceive(func([]byte))               {}
func (fakeRelay) ResendSubscriptions()                    {}
func (fakeRelay) ConfigureRelays([]transport.RelayConfig) {}
func (fakeRelay) Broadcast([]byte)                        {}
func (fakeRelay) BufferedAmount() uint64                  { return 0 }
func (fakeRelay) HasConnection() bool                     { return false }
func (fakeRelay) ConnectedCount() int                     { return 0 }
func (fakeRelay) Cleanup()                                {}

// connectedRelay simula um relay já conectado para que o accept ative a chamada.
type connectedRelay struct{ fakeRelay }

func (connectedRelay) HasConnection() bool { return true }

// deviceSock estende fakeSock devolvendo uma lista fixa de devices do destino.
type deviceSock struct {
	fakeSock
	devices []types.JID
	lidMap  map[string]types.JID // PN string -> LID
}

func (d *deviceSock) GetUSyncDevices(context.Context, []types.JID) ([]types.JID, error) {
	return d.devices, nil
}

func (d *deviceSock) ResolveLIDForPN(_ context.Context, pn types.JID) types.JID {
	if d.lidMap != nil {
		if lid, ok := d.lidMap[pn.String()]; ok {
			return lid
		}
	}
	return pn
}

func lidDevice(user string, device uint16) types.JID {
	j := types.NewJID(user, types.HiddenUserServer)
	j.Device = device
	return j
}

// ringingManager monta um CallManager com uma chamada de saída em Ringing e a
// lista de devices do destino já preenchida (como fica após o offer).
func ringingManager(sock core.VoipSocket, callee []types.JID) *CallManager {
	m := NewCallManager(sock, slog.Default())
	m.relay = fakeRelay{}
	call := NewOutgoingCall("CALL1", "62440234549366@lid", "caller@lid", core.CallMediaTypeAudio)
	_ = call.ApplyTransition(Transition{Type: TransitionOfferSent}) // → Ringing
	call.EncryptionKey = make([]byte, 32)
	m.currentCall = call
	m.calleeDevices = callee
	return m
}

func acceptNode(callID string, from types.JID) *waBinary.Node {
	return &waBinary.Node{
		Tag:   "call",
		Attrs: waBinary.Attrs{"from": from},
		Content: []waBinary.Node{{
			Tag:   "accept",
			Attrs: waBinary.Attrs{"call-id": callID, "call-creator": "caller@lid"},
		}},
	}
}

func terminateNode(callID string, from types.JID) *waBinary.Node {
	return &waBinary.Node{
		Tag:   "call",
		Attrs: waBinary.Attrs{"from": from},
		Content: []waBinary.Node{{
			Tag:   "terminate",
			Attrs: waBinary.Attrs{"call-id": callID, "call-creator": "caller@lid"},
		}},
	}
}

// elsewhereDests devolve os jids do <destination> do primeiro terminate
// "accepted_elsewhere" enviado, ou nil se não houve.
func elsewhereDests(sent []waBinary.Node) ([]string, bool) {
	for i := range sent {
		kids := wanode.NodeChildren(&sent[i])
		if len(kids) == 0 {
			continue
		}
		inner := kids[0]
		if inner.Tag != "terminate" || wanode.AttrString(inner.Attrs, "reason") != "accepted_elsewhere" {
			continue
		}
		var dests []string
		for _, c := range wanode.NodeChildren(&inner) {
			if c.Tag != "destination" {
				continue
			}
			for _, to := range wanode.NodeChildren(&c) {
				dests = append(dests, wanode.AttrString(to.Attrs, "jid"))
			}
		}
		return dests, true
	}
	return nil, false
}

func TestStartCallPersistsCalleeDevices(t *testing.T) {
	devs := []types.JID{lidDevice("62440234549366", 0), lidDevice("62440234549366", 33)}
	sock := &deviceSock{devices: devs}
	m := NewCallManager(sock, slog.Default())
	m.relay = fakeRelay{}
	t.Cleanup(m.cleanupMedia)

	if err := m.StartCall(context.Background(), "CALL1", lidDevice("62440234549366", 0), false); err != nil {
		t.Fatalf("start call: %v", err)
	}
	m.mu.Lock()
	got := len(m.calleeDevices)
	m.mu.Unlock()
	if got != 2 {
		t.Fatalf("calleeDevices = %d, want 2", got)
	}
}

func TestCompanionAcceptStopsSiblingDevices(t *testing.T) {
	primary := lidDevice("62440234549366", 0)
	companion := lidDevice("62440234549366", 33)
	sock := &deviceSock{devices: []types.JID{primary, companion}}
	m := ringingManager(sock, []types.JID{primary, companion})

	m.HandleCallAccept(context.Background(), acceptNode("CALL1", companion), companion)

	dests, ok := elsewhereDests(sock.sent)
	if !ok {
		t.Fatalf("nenhum terminate accepted_elsewhere enviado; tags=%v", sock.sentTags())
	}
	if len(dests) != 1 || dests[0] != primary.String() {
		t.Fatalf("destination deve listar só o primary %s, veio %v", primary, dests)
	}
}

func TestAcceptWithoutSiblingsSendsNoElsewhere(t *testing.T) {
	only := lidDevice("62440234549366", 0)
	sock := &deviceSock{devices: []types.JID{only}}
	m := ringingManager(sock, []types.JID{only})

	m.HandleCallAccept(context.Background(), acceptNode("CALL1", only), only)

	if _, ok := elsewhereDests(sock.sent); ok {
		t.Fatal("destino de device único não deve receber accepted_elsewhere")
	}
}

func TestSecondAcceptFromAnotherDeviceIsIgnored(t *testing.T) {
	primary := lidDevice("62440234549366", 0)
	companion := lidDevice("62440234549366", 33)
	sock := &deviceSock{devices: []types.JID{primary, companion}}
	m := ringingManager(sock, []types.JID{primary, companion})

	m.HandleCallAccept(context.Background(), acceptNode("CALL1", companion), companion)
	before := len(sock.sent)
	m.HandleCallAccept(context.Background(), acceptNode("CALL1", primary), primary)

	m.mu.Lock()
	accepted := m.acceptedByJid
	m.mu.Unlock()
	if accepted != companion.String() {
		t.Fatalf("first accept must win: acceptedByJid=%q, want %q", accepted, companion)
	}
	if after := len(sock.sent); after != before {
		t.Fatalf("accept duplicado não deve enviar stanzas: %d -> %d", before, after)
	}
}

func TestSameDeviceAcceptRetryDoesNotRepeatElsewhereFanout(t *testing.T) {
	primary := lidDevice("62440234549366", 0)
	companion := lidDevice("62440234549366", 33)
	sock := &deviceSock{devices: []types.JID{primary, companion}}
	m := ringingManager(sock, []types.JID{primary, companion})

	m.HandleCallAccept(context.Background(), acceptNode("CALL1", companion), companion)
	m.HandleCallAccept(context.Background(), acceptNode("CALL1", companion), companion)

	elsewheres := 0
	for i := range sock.sent {
		kids := wanode.NodeChildren(&sock.sent[i])
		if len(kids) > 0 && wanode.AttrString(kids[0].Attrs, "reason") == "accepted_elsewhere" {
			elsewheres++
		}
	}
	if elsewheres != 1 {
		t.Fatalf("fan-out elsewhere deve sair exatamente uma vez, veio %d", elsewheres)
	}
}

func TestAcceptOnInboundLegIsIgnored(t *testing.T) {
	primary := lidDevice("62440234549366", 0)
	caller := lidDevice("24017624899709", 17)
	sock := &deviceSock{devices: []types.JID{primary}}
	m := NewCallManager(sock, slog.Default())
	m.relay = fakeRelay{}
	m.currentCall = NewIncomingCall("CALL1", caller.String(), caller.String(), "", core.CallMediaTypeAudio)
	m.calleeDevices = []types.JID{primary}

	m.HandleCallAccept(context.Background(), acceptNode("CALL1", caller), caller)

	if !m.CurrentCall().IsEnded() {
		t.Fatal("accept em chamada inbound deve encerrar o leg local (answered elsewhere)")
	}
	if m.CurrentCall().StateData.EndReason != core.EndCallReasonAcceptedElsewhere {
		t.Fatalf("reason deve ser accepted_elsewhere, veio %q", m.CurrentCall().StateData.EndReason)
	}
}

func TestLateRejectFromNonAnsweringDeviceKeepsCall(t *testing.T) {
	primary := lidDevice("62440234549366", 0)
	companion := lidDevice("62440234549366", 33)
	sock := &deviceSock{devices: []types.JID{primary, companion}}
	m := ringingManager(sock, []types.JID{primary, companion})

	m.HandleCallAccept(context.Background(), acceptNode("CALL1", companion), companion)
	m.HandleCallTerminate(terminateNode("CALL1", primary))
	if m.CurrentCall().IsEnded() {
		t.Fatal("terminate do device que não atendeu não pode encerrar a chamada")
	}

	m.HandleCallTerminate(terminateNode("CALL1", companion))
	if !m.CurrentCall().IsEnded() {
		t.Fatal("terminate do device que atendeu deve encerrar a chamada")
	}
}

// ---------------------------------------------------------------------------
// Regressão: outbound PN/LID e dupla sessão local
// ---------------------------------------------------------------------------

func TestOutboundWithPNStartsWithResolvedLID(t *testing.T) {
	pn := types.JID{User: "553488775588", Server: types.DefaultUserServer}
	lid := lidDevice("49989292171350", 0)
	sock := &deviceSock{
		devices: []types.JID{lid},
		lidMap:  map[string]types.JID{pn.String(): lid},
	}
	m := NewCallManager(sock, slog.Default())
	m.relay = fakeRelay{}
	t.Cleanup(m.cleanupMedia)

	if err := m.StartCall(context.Background(), "CID", pn, false); err != nil {
		t.Fatalf("start call: %v", err)
	}
	if got := m.CurrentCall().PeerJid; got != lid.String() {
		t.Fatalf("outbound com PN deve resolver para LID: peer=%q, want %q", got, lid)
	}
}

func TestOutboundWithLIDStartsDirectly(t *testing.T) {
	lid := lidDevice("49989292171350", 0)
	sock := &deviceSock{devices: []types.JID{lid}}
	m := NewCallManager(sock, slog.Default())
	m.relay = fakeRelay{}
	t.Cleanup(m.cleanupMedia)

	if err := m.StartCall(context.Background(), "CID", lid, false); err != nil {
		t.Fatalf("start call: %v", err)
	}
	if got := m.CurrentCall().PeerJid; got != lid.String() {
		t.Fatalf("outbound com LID deve manter LID: peer=%q, want %q", got, lid)
	}
}

// TestSingleCallCannotOwnTwoLocalSessions reproduz o bug da call
// A68D3997844AFEDDC9FD6F2E12AE0C26: o mesmo servidor hospedava o leg de origem
// (outbound) e o leg de destino (inbound) da mesma chamada. O accept do destino
// não pode criar uma segunda sessão proprietária local.
func TestSingleCallCannotOwnTwoLocalSessions(t *testing.T) {
	callID := "A68D3997844AFEDDC9FD6F2E12AE0C26"
	caller := lidDevice("24017624899709", 17)
	callee := lidDevice("49989292171350", 0)

	// Sessão do originador (outbound) — a única que pode processar o accept.
	originSock := &deviceSock{devices: []types.JID{callee}}
	origin := NewCallManager(originSock, slog.Default())
	origin.relay = connectedRelay{}
	origin.currentCall = NewOutgoingCall(callID, callee.String(), caller.String(), core.CallMediaTypeAudio)
	_ = origin.currentCall.ApplyTransition(Transition{Type: TransitionOfferSent})
	origin.calleeDevices = []types.JID{callee}

	// Sessão do destinatário (inbound) — mesma call_id, mas deve ignorar o accept.
	inboundSock := &deviceSock{devices: []types.JID{callee}}
	inbound := NewCallManager(inboundSock, slog.Default())
	inbound.relay = fakeRelay{}
	inbound.currentCall = NewIncomingCall(callID, caller.String(), caller.String(), "", core.CallMediaTypeAudio)

	// O accept chega primeiro no leg inbound (sincronização do WhatsApp).
	inbound.HandleCallAccept(context.Background(), acceptNode(callID, caller), caller)
	if !inbound.CurrentCall().IsEnded() {
		t.Fatal("accept no leg inbound deve encerrar a sessão local (answered elsewhere)")
	}
	if inbound.CurrentCall().StateData.EndReason != core.EndCallReasonAcceptedElsewhere {
		t.Fatalf("leg inbound deve terminar com accepted_elsewhere, veio %q", inbound.CurrentCall().StateData.EndReason)
	}

	// O accept chega depois no leg originador e estabelece a chamada.
	origin.HandleCallAccept(context.Background(), acceptNode(callID, callee), callee)
	if !origin.CurrentCall().IsActive() {
		t.Fatalf("leg originador deve ficar ativo após accept: state=%q", origin.CurrentCall().StateData.State)
	}
}

func TestOutboundAcceptKeepsMediaStateActive(t *testing.T) {
	primary := lidDevice("62440234549366", 0)
	sock := &deviceSock{devices: []types.JID{primary}}
	m := NewCallManager(sock, slog.Default())
	m.relay = connectedRelay{}
	call := NewOutgoingCall("CALL1", primary.String(), "caller@lid", core.CallMediaTypeAudio)
	_ = call.ApplyTransition(Transition{Type: TransitionOfferSent})
	call.EncryptionKey = make([]byte, 32)
	m.currentCall = call
	m.calleeDevices = []types.JID{primary}

	m.HandleCallAccept(context.Background(), acceptNode("CALL1", primary), primary)

	if !m.CurrentCall().IsActive() {
		t.Fatalf("outbound deve ficar active após accept com relay conectado: state=%q", m.CurrentCall().StateData.State)
	}
}

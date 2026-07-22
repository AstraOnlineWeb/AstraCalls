package call

import (
	"context"
	"sync"
	"testing"

	"wacalls/internal/voip/core"

	waBinary "go.mau.fi/whatsmeow/binary"
	"go.mau.fi/whatsmeow/types"
)

// fakeSock implementa core.VoipSocket registrando só o que os testes de vídeo
// precisam: as stanzas passadas a SendNode. O resto retorna valores zero.
type fakeSock struct {
	mu   sync.Mutex
	sent []waBinary.Node
}

func (f *fakeSock) OwnPN() types.JID  { return types.JID{User: "999", Server: types.DefaultUserServer} }
func (f *fakeSock) OwnLID() types.JID { return types.JID{User: "999", Server: types.HiddenUserServer} }
func (f *fakeSock) AccountDeviceIdentityNode() (waBinary.Node, bool) {
	return waBinary.Node{}, false
}
func (f *fakeSock) SendNode(_ context.Context, node waBinary.Node) error {
	f.mu.Lock()
	f.sent = append(f.sent, node)
	f.mu.Unlock()
	return nil
}
func (f *fakeSock) Query(context.Context, waBinary.Node) (*waBinary.Node, error) { return nil, nil }
func (f *fakeSock) GetUSyncDevices(context.Context, []types.JID) ([]types.JID, error) {
	return nil, nil
}
func (f *fakeSock) AssertSessions(context.Context, []types.JID, bool) error { return nil }
func (f *fakeSock) CreateParticipantNodes(context.Context, []types.JID, []byte, waBinary.Attrs) ([]waBinary.Node, bool, error) {
	return nil, false, nil
}
func (f *fakeSock) DecryptCallKey(context.Context, types.JID, *waBinary.Node) ([]byte, error) {
	return nil, nil
}
func (f *fakeSock) GetTCToken(context.Context, types.JID) ([]byte, error) { return nil, nil }
func (f *fakeSock) ResolveLIDForPN(_ context.Context, pn types.JID) types.JID { return pn }

func (f *fakeSock) sentTags() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, n := range f.sent {
		out = append(out, n.Tag)
	}
	return out
}

func (f *fakeSock) videoStateSent() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var states []string
	for _, n := range f.sent {
		if n.Tag != "call" {
			continue
		}
		kids, _ := n.Content.([]waBinary.Node)
		for _, k := range kids {
			if k.Tag == "video" {
				if s, ok := k.Attrs["state"].(string); ok {
					states = append(states, s)
				}
			}
		}
	}
	return states
}

func activeManager(sock core.VoipSocket) (*CallManager, *CallInfo) {
	m := NewCallManager(sock, nil)
	call := NewIncomingCall("CID", testPeerJID().String(), testPeerJID().String(), "", core.CallMediaTypeAudio)
	call.StateData.State = core.CallStateActive
	m.currentCall = call
	return m, call
}

func testPeerJID() types.JID { return types.JID{User: "111", Server: types.DefaultUserServer} }

func incomingVideoNode(state string) *waBinary.Node {
	return &waBinary.Node{
		Tag:   "call",
		Attrs: waBinary.Attrs{"from": testPeerJID(), "id": "wrap"},
		Content: []waBinary.Node{{
			Tag:   "video",
			Attrs: waBinary.Attrs{"call-id": "CID", "state": state},
		}},
	}
}

// Um pedido de upgrade (state=11) recebido deve: mandar o ack, marcar
// VideoUpgradeIncoming e disparar OnVideoUpgradeRequest.
func TestHandleVideoUpgradeRequest(t *testing.T) {
	sock := &fakeSock{}
	m, call := activeManager(sock)
	var fired bool
	m.OnVideoUpgradeRequest = func(*CallInfo) { fired = true }

	m.HandleVideoState(context.Background(), incomingVideoNode("11"))

	if !fired {
		t.Error("OnVideoUpgradeRequest não disparou")
	}
	if !call.StateData.VideoUpgradeIncoming {
		t.Error("VideoUpgradeIncoming deveria estar true")
	}
	if tags := sock.sentTags(); len(tags) != 1 || tags[0] != "ack" {
		t.Errorf("esperava só o ack, veio %v", tags)
	}
}

// Quando o peer aceita o upgrade que pedimos (state=4), ligamos a câmera e
// confirmamos com state=1; o ack também é enviado.
func TestHandleVideoUpgradeAccept(t *testing.T) {
	sock := &fakeSock{}
	m, call := activeManager(sock)
	call.StateData.VideoUpgradeOutgoing = true

	m.HandleVideoState(context.Background(), incomingVideoNode("4"))

	if call.StateData.VideoOff {
		t.Error("VideoOff deveria virar false após aceite do peer")
	}
	if call.MediaType != core.CallMediaTypeVideo {
		t.Error("MediaType deveria virar video")
	}
	if call.StateData.VideoUpgradeOutgoing {
		t.Error("VideoUpgradeOutgoing deveria zerar após aceite")
	}
	// ack + <video state=1>
	if states := sock.videoStateSent(); len(states) != 1 || states[0] != "1" {
		t.Errorf("esperava confirmar com state=1, veio %v", states)
	}
}

// Fora de uma chamada ativa, ainda confirmamos com o ack (o WhatsApp o exige),
// mas nenhuma mudança de estado nem stanza de resposta de vídeo é gerada.
func TestHandleVideoStateIgnoraSemCallAtiva(t *testing.T) {
	sock := &fakeSock{}
	m, call := activeManager(sock)
	call.StateData.State = core.CallStateRinging // não ativa

	m.HandleVideoState(context.Background(), incomingVideoNode("11"))

	if tags := sock.sentTags(); len(tags) != 1 || tags[0] != "ack" {
		t.Errorf("esperava só o ack, veio %v", tags)
	}
	if states := sock.videoStateSent(); len(states) != 0 {
		t.Errorf("não deveria responder com stanza de vídeo fora de call ativa, veio %v", states)
	}
	if call.StateData.VideoUpgradeIncoming {
		t.Error("não deveria marcar upgrade fora de call ativa")
	}
}

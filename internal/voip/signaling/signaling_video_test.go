package signaling

import (
	"testing"

	waBinary "go.mau.fi/whatsmeow/binary"
	"go.mau.fi/whatsmeow/types"
)

func videoChild(t *testing.T, call waBinary.Node) waBinary.Node {
	t.Helper()
	kids, ok := call.Content.([]waBinary.Node)
	if !ok || len(kids) != 1 || kids[0].Tag != "video" {
		t.Fatalf("esperava um filho <video>, veio %#v", call.Content)
	}
	return kids[0]
}

func attr(n waBinary.Node, k string) string {
	if v, ok := n.Attrs[k]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

func testPeer() types.JID    { return types.JID{User: "111", Server: types.DefaultUserServer} }
func testCreator() types.JID { return types.JID{User: "222", Server: types.DefaultUserServer} }

// O pedido de upgrade atual (state=11) precisa levar dec=H264, voip_settings=video
// e device_orientation — sem isso o WhatsApp Web ignora.
func TestUpgradeRequestV2Shape(t *testing.T) {
	orientation := 0
	call := BuildVideoStateStanza(VideoStateParams{
		CallID: "CID", To: testPeer(), CallCreator: testCreator(),
		State: VideoStateUpgradeRequestV2, Dec: VideoDecRequest, DeviceOrientation: &orientation,
	})
	v := videoChild(t, call)
	if got := attr(v, "state"); got != "11" {
		t.Errorf("state = %q, quer 11", got)
	}
	if got := attr(v, "dec"); got != "H264" {
		t.Errorf("dec = %q, quer H264", got)
	}
	if got := attr(v, "voip_settings"); got != "video" {
		t.Errorf("voip_settings = %q, quer video", got)
	}
	if got := attr(v, "device_orientation"); got != "0" {
		t.Errorf("device_orientation = %q, quer 0", got)
	}
}

// O accept do upgrade leva dec=H264,AV1; o enabled (câmera on) NÃO leva dec.
func TestUpgradeAcceptAndEnabledShapes(t *testing.T) {
	accept := BuildVideoStateStanza(VideoStateParams{
		CallID: "CID", To: testPeer(), CallCreator: testCreator(),
		State: VideoStateUpgradeAccept, Dec: VideoDecAccept,
	})
	if got := attr(videoChild(t, accept), "dec"); got != "H264,AV1" {
		t.Errorf("accept dec = %q, quer H264,AV1", got)
	}
	if got := attr(videoChild(t, accept), "voip_settings"); got != "" {
		t.Errorf("accept não deve levar voip_settings, veio %q", got)
	}

	enabled := BuildVideoStateStanza(VideoStateParams{
		CallID: "CID", To: testPeer(), CallCreator: testCreator(), State: VideoStateEnabled,
	})
	if _, has := videoChild(t, enabled).Attrs["dec"]; has {
		t.Error("enabled (state=1) não deve carregar dec")
	}
	if got := attr(videoChild(t, enabled), "state"); got != "1" {
		t.Errorf("enabled state = %q, quer 1", got)
	}
}

// device_orientation só entra quando fornecido (ponteiro não-nil).
func TestDeviceOrientationOptional(t *testing.T) {
	without := BuildVideoStateStanza(VideoStateParams{
		CallID: "CID", To: testPeer(), CallCreator: testCreator(), State: VideoStateUpgradeReject,
	})
	if _, has := videoChild(t, without).Attrs["device_orientation"]; has {
		t.Error("sem ponteiro, device_orientation não deveria aparecer")
	}
}

// Todo <video> recebido exige ack tipado (class=call type=video) preservando o
// roteamento p/ dispositivo companion.
func TestBuildVideoAckPreservaRoteamento(t *testing.T) {
	from := types.JID{User: "111", Server: types.DefaultUserServer}
	participant := types.JID{User: "333", Server: types.HiddenUserServer}
	original := &waBinary.Node{Tag: "call", Attrs: waBinary.Attrs{
		"id": "wrap", "from": from, "participant": participant,
	}}
	ack, ok := BuildVideoAck(original)
	if !ok {
		t.Fatal("BuildVideoAck rejeitou stanza roteável")
	}
	if attr(ack, "class") != "call" || attr(ack, "type") != "video" {
		t.Errorf("ack class/type errados: %#v", ack.Attrs)
	}
	if ack.Attrs["participant"] != participant {
		t.Errorf("participant = %v, quer %v", ack.Attrs["participant"], participant)
	}
}

// Sem id/from não há como rotear o ack — deve falhar de forma limpa.
func TestBuildVideoAckSemRoteamento(t *testing.T) {
	if _, ok := BuildVideoAck(&waBinary.Node{Tag: "call"}); ok {
		t.Error("ack sem id/from deveria retornar ok=false")
	}
	if _, ok := BuildVideoAck(nil); ok {
		t.Error("ack de node nil deveria retornar ok=false")
	}
}

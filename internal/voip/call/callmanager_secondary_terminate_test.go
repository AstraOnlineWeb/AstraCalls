package call

import (
	"log/slog"
	"testing"

	"wacalls/internal/voip/core"

	waBinary "go.mau.fi/whatsmeow/binary"
)

const (
	testIncomingCallID  = "INCOMING1"
	testIncomingPeer    = "13035645218872@lid"
	testHostedSecondary = "173693040889958:99@hosted.lid"
)

func incomingRingingManager() *CallManager {
	m := NewCallManager(&fakeSock{}, slog.Default())
	m.relay = fakeRelay{}
	m.currentCall = NewIncomingCall(
		testIncomingCallID,
		testIncomingPeer,
		testIncomingPeer,
		"556281242283@s.whatsapp.net",
		core.CallMediaTypeAudio,
	)
	return m
}

func terminalCallNode(tag, from, platform, reason string) *waBinary.Node {
	attrs := waBinary.Attrs{
		"call-id":      testIncomingCallID,
		"call-creator": testIncomingPeer,
	}
	if reason != "" {
		attrs["reason"] = reason
	}
	return &waBinary.Node{
		Tag: "call",
		Attrs: waBinary.Attrs{
			"from":     from,
			"platform": platform,
		},
		Content: []waBinary.Node{{Tag: tag, Attrs: attrs}},
	}
}

func TestIncomingUncallableFromSecondaryHostedLIDIsIgnored(t *testing.T) {
	m := incomingRingingManager()
	ended := false
	m.OnEnded = func(*CallInfo) { ended = true }

	m.HandleCallTerminate(terminalCallNode(
		"reject", testHostedSecondary, "", "uncallable",
	))

	if ended {
		t.Fatal("secondary hosted uncallable must not fire OnEnded")
	}
	if !m.CurrentCall().CanAccept() {
		t.Fatalf("incoming call must remain answerable, state=%s", m.CurrentCall().StateData.State)
	}
}

func TestIncomingTerminateFromOriginalCallerStillEndsCall(t *testing.T) {
	m := incomingRingingManager()
	ended := false
	m.OnEnded = func(*CallInfo) { ended = true }

	m.HandleCallTerminate(terminalCallNode(
		"terminate", testIncomingPeer, "android", "",
	))

	if !ended {
		t.Fatal("original caller terminate must fire OnEnded")
	}
	if !m.CurrentCall().IsEnded() {
		t.Fatalf("original caller terminate must end call, state=%s", m.CurrentCall().StateData.State)
	}
}

func TestUncallableIgnoreRuleIsNarrow(t *testing.T) {
	tests := []struct {
		name     string
		from     string
		platform string
		reason   string
	}{
		{name: "original caller", from: testIncomingPeer, platform: "android", reason: "uncallable"},
		{name: "non hosted CAPI device", from: "173693040889958:99@lid", platform: "capi", reason: "uncallable"},
		{name: "different reason", from: testHostedSecondary, platform: "capi", reason: "declined"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := incomingRingingManager()
			m.HandleCallTerminate(terminalCallNode(
				"reject", tt.from, tt.platform, tt.reason,
			))
			if !m.CurrentCall().IsEnded() {
				t.Fatalf("event outside narrow ignore rule must end call, state=%s", m.CurrentCall().StateData.State)
			}
		})
	}
}

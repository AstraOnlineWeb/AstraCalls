package video

import (
	"sync"
	"testing"

	"wacalls/internal/voip/transport"
)

type fakeRelay struct {
	mu         sync.Mutex
	broadcasts int
	connected  bool
	buffered   uint64
}

func (f *fakeRelay) Broadcast(data []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.broadcasts++
}
func (f *fakeRelay) BufferedAmount() uint64             { return f.buffered }
func (f *fakeRelay) HasConnection() bool                { return f.connected }
func (f *fakeRelay) SetStreamSsrcs(self, peer []uint32) {}

func TestFeedCapturedNoopBeforeSetup(t *testing.T) {
	r := &fakeRelay{connected: true}
	p := New(nil, r)
	p.FeedCaptured([]byte{0, 0, 0, 1, 0x65, 0x01, 0x02})
	if r.broadcasts != 0 {
		t.Fatalf("expected no broadcast before Setup, got %d", r.broadcasts)
	}
}

func TestHandleRelayDataNoopBeforeSetup(t *testing.T) {
	r := &fakeRelay{connected: true}
	p := New(nil, r)
	emitted := false
	p.OnFrame = func([]byte) { emitted = true }
	p.HandleRelayData(make([]byte, 16))
	if emitted {
		t.Fatal("expected no frame emitted before Setup")
	}
}

func TestResetClearsState(t *testing.T) {
	p := New(nil, &fakeRelay{})
	p.depack = &transport.H264Depacketizer{}
	p.frameBuf = []byte{1, 2, 3}
	p.selfSsrc = 42
	p.Reset()
	if p.depack != nil || p.frameBuf != nil || p.selfSsrc != 0 {
		t.Fatal("Reset did not clear pipeline state")
	}
}

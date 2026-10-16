package main

import (
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media"
)

// O "navegador" aqui é um peer pion que oferece Opus + PCMU, como o Chrome. O
// conector tem que escolher PCMU e o áudio tem que passar nos dois sentidos.
func TestWebRTCLegNegotiatesPCMUBothWays(t *testing.T) {
	api := getWebRTCAPI(config{PublicIP: "127.0.0.1"}, slog.New(slog.NewTextHandler(os.Stderr, nil)))

	m := &webrtc.MediaEngine{}
	if err := m.RegisterDefaultCodecs(); err != nil {
		t.Fatal(err)
	}
	browser, err := webrtc.NewAPI(webrtc.WithMediaEngine(m)).NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		t.Fatal(err)
	}
	defer browser.Close()
	mic, _ := webrtc.NewTrackLocalStaticSample(
		webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypePCMU, ClockRate: 8000, Channels: 1}, "mic", "browser")
	if _, err := browser.AddTrack(mic); err != nil {
		t.Fatal(err)
	}
	if _, err := browser.CreateDataChannel("h264", nil); err != nil { // o softphone sempre abre
		t.Fatal(err)
	}
	heard := make(chan string, 1)
	browser.OnTrack(func(tr *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		if _, _, err := tr.ReadRTP(); err == nil {
			heard <- tr.Codec().MimeType
		}
	})

	offer, _ := browser.CreateOffer(nil)
	gathered := webrtc.GatheringCompletePromise(browser)
	_ = browser.SetLocalDescription(offer)
	<-gathered

	fromBrowser := make(chan []byte, 8)
	leg, answer, err := newWebRTCLeg(api, browser.LocalDescription().SDP,
		func(p []byte) {
			select {
			case fromBrowser <- p:
			default:
			}
		}, func() {})
	if err != nil {
		t.Fatal(err)
	}
	defer leg.close()
	if err := browser.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: answer}); err != nil {
		t.Fatal(err)
	}

	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	timeout := time.After(10 * time.Second)
	frame := make([]byte, frameBytes8k)
	gotUp, gotDown := false, false
	for !gotUp || !gotDown {
		select {
		case <-tick.C:
			_ = mic.WriteSample(media.Sample{Data: frame, Duration: 20 * time.Millisecond})
			leg.writePCMU(frame)
		case p := <-fromBrowser:
			if len(p) != frameBytes8k {
				t.Fatalf("quadro do navegador com %d bytes", len(p))
			}
			gotUp = true
		case mime := <-heard:
			if mime != webrtc.MimeTypePCMU {
				t.Fatalf("navegador recebeu %s", mime)
			}
			gotDown = true
		case <-timeout:
			t.Fatalf("áudio não passou: navegador->conector=%v conector->navegador=%v", gotUp, gotDown)
		}
	}
}

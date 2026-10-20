package main

import (
	"log/slog"
	"net"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media"
)

// browserAPI é inicializado uma única vez. Quando WACALLS_PUBLIC_IP e
// WACALLS_UDP_PORT estão definidos, o WebRTC com o navegador usa porta fixa
// (UDP + ICE-TCP na mesma porta) e anuncia o IP público como candidato (NAT 1:1).
// O ICE-TCP garante a mídia mesmo quando UDP de entrada é bloqueado.
var (
	browserAPIOnce sync.Once
	browserAPI     *webrtc.API
)

func getBrowserAPI(log *slog.Logger) *webrtc.API {
	browserAPIOnce.Do(func() {
		// IP fixo ou "auto" (descoberta por interface → STUN → HTTPS; ver publicip.go)
		publicIP := resolvedPublicIP(log)
		udpPort, _ := strconv.Atoi(os.Getenv("WACALLS_UDP_PORT"))
		if publicIP == "" || udpPort == 0 {
			browserAPI = webrtc.NewAPI()
			return
		}
		se := webrtc.SettingEngine{}
		se.SetNAT1To1IPs([]string{publicIP}, webrtc.ICECandidateTypeHost)
		se.SetNetworkTypes([]webrtc.NetworkType{
			webrtc.NetworkTypeUDP4, webrtc.NetworkTypeUDP6,
			webrtc.NetworkTypeTCP4, webrtc.NetworkTypeTCP6,
		})
		udpConn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4zero, Port: udpPort})
		if err != nil {
			log.Error("browser webrtc: bind udp mux failed; falling back to ephemeral", "port", udpPort, "err", err)
			browserAPI = webrtc.NewAPI()
			return
		}
		se.SetICEUDPMux(webrtc.NewICEUDPMux(nil, udpConn))
		log.Info("browser webrtc: fixed udp port + nat1to1 enabled", "public_ip", publicIP, "udp_port", udpPort)
		if tcpListener, terr := net.ListenTCP("tcp", &net.TCPAddr{IP: net.IPv4zero, Port: udpPort}); terr == nil {
			se.SetICETCPMux(webrtc.NewICETCPMux(nil, tcpListener, 8))
			log.Info("browser webrtc: ice-tcp fallback enabled", "tcp_port", udpPort)
		} else {
			log.Error("browser webrtc: ice-tcp bind failed", "port", udpPort, "err", terr)
		}
		browserAPI = webrtc.NewAPI(webrtc.WithSettingEngine(se))
	})
	return browserAPI
}

// h264ChannelLabel is the data channel the browser opens to carry H264 access
// units for video calls in both directions. The browser side must create it with
// this label. (Audio uses the Opus RTP media track, not a data channel.)
const h264ChannelLabel = "h264"

type Bridge struct {
	pc         *webrtc.PeerConnection
	localTrack *webrtc.TrackLocalStaticSample
	videoDC    atomic.Pointer[webrtc.DataChannel]
	log        *slog.Logger

	OnBrowserRTP func(payload []byte)
	// OnBrowserVideo is invoked with H264 access units captured from the browser camera.
	OnBrowserVideo func(au []byte)
	// OnTerminalICE fires when the peer connection fails or closes.
	OnTerminalICE func()
	// terminateDisabled silencia o OnTerminalICE. Usado ao TROCAR a ponte (ex.:
	// renegociação ou transferência): fechar a ponte antiga não deve encerrar a
	// chamada, pois a nova ponte assume o mesmo leg.
	terminateDisabled atomic.Bool
	// OnICEState (opcional) recebe cada mudança de estado do ICE (log/diagnóstico).
	OnICEState func(state string)
	// connected marca que o ICE chegou a conectar ao menos uma vez.
	connected atomic.Bool
}

// WasConnected diz se o ICE desta ponte chegou a conectar.
func (b *Bridge) WasConnected() bool { return b.connected.Load() }

// DisableTerminate impede que o fechamento desta ponte dispare OnTerminalICE.
func (b *Bridge) DisableTerminate() { b.terminateDisabled.Store(true) }

func NewBridge(offerSDP string, log *slog.Logger) (*Bridge, string, error) {
	pc, err := getBrowserAPI(log).NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		return nil, "", err
	}

	br := &Bridge{pc: pc, log: log}

	localTrack, err := webrtc.NewTrackLocalStaticSample(

		webrtc.RTPCodecCapability{
			MimeType:    webrtc.MimeTypeOpus,
			ClockRate:   48000,
			Channels:    2,
			SDPFmtpLine: "minptime=10;useinbandfec=1",
		},
		"audio", "wacalls",
	)
	if err != nil {
		pc.Close()
		return nil, "", err
	}
	br.localTrack = localTrack

	pc.OnTrack(func(tr *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		go func() {
			for {
				pkt, _, err := tr.ReadRTP()
				if err != nil {
					return
				}
				if br.OnBrowserRTP != nil && len(pkt.Payload) > 0 {
					br.OnBrowserRTP(pkt.Payload)
				}
			}
		}()
	})

	// Video: the browser opens an "h264" data channel carrying H264 access units
	// in both directions. Audio stays on the Opus RTP media track above.
	pc.OnDataChannel(func(dc *webrtc.DataChannel) {
		if dc.Label() != h264ChannelLabel {
			return
		}
		br.videoDC.Store(dc)
		dc.OnMessage(func(msg webrtc.DataChannelMessage) {
			if cb := br.OnBrowserVideo; cb != nil && len(msg.Data) > 0 {
				cb(msg.Data)
			}
		})
	})

	pc.OnICEConnectionStateChange(func(s webrtc.ICEConnectionState) {
		log.Debug("browser ice state", "state", s.String())
		if s == webrtc.ICEConnectionStateConnected {
			br.connected.Store(true)
		}
		if br.OnICEState != nil {
			br.OnICEState(s.String())
		}
		if s == webrtc.ICEConnectionStateFailed || s == webrtc.ICEConnectionStateClosed {
			if br.OnTerminalICE != nil && !br.terminateDisabled.Load() {
				br.OnTerminalICE()
			}
		}
	})

	if err := pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: offerSDP}); err != nil {
		pc.Close()
		return nil, "", err
	}
	if _, err := pc.AddTrack(localTrack); err != nil {
		pc.Close()
		return nil, "", err
	}
	answer, err := pc.CreateAnswer(nil)
	if err != nil {
		pc.Close()
		return nil, "", err
	}
	gatherComplete := webrtc.GatheringCompletePromise(pc)
	if err := pc.SetLocalDescription(answer); err != nil {
		pc.Close()
		return nil, "", err
	}
	<-gatherComplete

	return br, pc.LocalDescription().SDP, nil
}

func (b *Bridge) WriteOpus(payload []byte, dur time.Duration) error {
	if b.localTrack == nil {
		return nil
	}
	return b.localTrack.WriteSample(media.Sample{Data: payload, Duration: dur})
}

func (b *Bridge) WriteVideo(au []byte) error {
	dc := b.videoDC.Load()
	if dc == nil || len(au) == 0 {
		return nil
	}
	return dc.Send(au)
}

func (b *Bridge) Close() {
	if b.pc != nil {
		_ = b.pc.Close()
	}
}

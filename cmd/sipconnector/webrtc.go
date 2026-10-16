package main

import (
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media"
)

// Perna WebRTC com o navegador do atendente (o mesmo /webrtc que o softphone do
// Botfy já usa com o wacalls). A diferença: aqui o ÚNICO codec registrado é PCMU,
// então o áudio do navegador vai para o PABX só reempacotado — sem transcodificar,
// sem Opus, sem CPU. O DataChannel "h264" que o softphone sempre abre é aceito e
// ignorado (não há vídeo em ligação de PABX).
var (
	webrtcAPIOnce sync.Once
	webrtcAPI     *webrtc.API
)

func getWebRTCAPI(cfg config, log *slog.Logger) *webrtc.API {
	webrtcAPIOnce.Do(func() {
		m := &webrtc.MediaEngine{}
		if err := m.RegisterCodec(webrtc.RTPCodecParameters{
			RTPCodecCapability: webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypePCMU, ClockRate: 8000, Channels: 1},
			PayloadType:        webrtc.PayloadType(ptPCMU),
		}, webrtc.RTPCodecTypeAudio); err != nil {
			log.Error("webrtc: registrar PCMU falhou", "err", err)
		}
		se := webrtc.SettingEngine{}
		se.SetNAT1To1IPs([]string{cfg.PublicIP}, webrtc.ICECandidateTypeHost)
		// Porta fixa (UDP + ICE-TCP na mesma porta) para publicar no Swarm; sem ela,
		// portas efêmeras (só serve em rede sem firewall).
		if cfg.WebRTCPort > 0 {
			se.SetNetworkTypes([]webrtc.NetworkType{
				webrtc.NetworkTypeUDP4, webrtc.NetworkTypeUDP6, webrtc.NetworkTypeTCP4, webrtc.NetworkTypeTCP6,
			})
			if udp, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4zero, Port: cfg.WebRTCPort}); err == nil {
				se.SetICEUDPMux(webrtc.NewICEUDPMux(nil, udp))
			} else {
				log.Error("webrtc: porta UDP fixa indisponível", "port", cfg.WebRTCPort, "err", err)
			}
			if tcp, err := net.ListenTCP("tcp", &net.TCPAddr{IP: net.IPv4zero, Port: cfg.WebRTCPort}); err == nil {
				se.SetICETCPMux(webrtc.NewICETCPMux(nil, tcp, 8))
			} else {
				log.Error("webrtc: porta TCP fixa indisponível", "port", cfg.WebRTCPort, "err", err)
			}
		}
		webrtcAPI = webrtc.NewAPI(webrtc.WithMediaEngine(m), webrtc.WithSettingEngine(se))
	})
	return webrtcAPI
}

type webrtcLeg struct {
	pc    *webrtc.PeerConnection
	track *webrtc.TrackLocalStaticSample

	// replaced: a perna foi trocada por outro consumidor; fechar não encerra a chamada
	replaced atomic.Bool
	closed   atomic.Bool
}

// newWebRTCLeg responde a oferta do navegador. onAudio recebe o PCMU que o
// navegador manda; onTerminal dispara quando a conexão cai (e não foi trocada).
func newWebRTCLeg(api *webrtc.API, offer string, onAudio func([]byte), onTerminal func()) (*webrtcLeg, string, error) {
	pc, err := api.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		return nil, "", err
	}
	track, err := webrtc.NewTrackLocalStaticSample(
		webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypePCMU, ClockRate: 8000, Channels: 1},
		"audio", "sipconnector",
	)
	if err != nil {
		_ = pc.Close()
		return nil, "", err
	}
	leg := &webrtcLeg{pc: pc, track: track}

	pc.OnTrack(func(tr *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		go func() {
			for {
				pkt, _, err := tr.ReadRTP()
				if err != nil {
					return
				}
				if len(pkt.Payload) > 0 {
					onAudio(pkt.Payload)
				}
			}
		}()
	})
	pc.OnDataChannel(func(*webrtc.DataChannel) {})
	pc.OnConnectionStateChange(func(s webrtc.PeerConnectionState) {
		if s == webrtc.PeerConnectionStateFailed || s == webrtc.PeerConnectionStateClosed {
			if !leg.replaced.Load() && leg.closed.CompareAndSwap(false, true) {
				onTerminal()
			}
		}
	})

	if err := pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: offer}); err != nil {
		_ = pc.Close()
		return nil, "", err
	}
	if _, err := pc.AddTrack(track); err != nil {
		_ = pc.Close()
		return nil, "", err
	}
	answer, err := pc.CreateAnswer(nil)
	if err != nil {
		_ = pc.Close()
		return nil, "", err
	}
	gathered := webrtc.GatheringCompletePromise(pc)
	if err := pc.SetLocalDescription(answer); err != nil {
		_ = pc.Close()
		return nil, "", err
	}
	<-gathered
	return leg, pc.LocalDescription().SDP, nil
}

// writePCMU manda para o navegador um quadro PCMU vindo do PABX.
func (w *webrtcLeg) writePCMU(payload []byte) {
	_ = w.track.WriteSample(media.Sample{
		Data:     payload,
		Duration: time.Duration(len(payload)) * time.Second / 8000,
	})
}

// close encerra sem disparar onTerminal (quem chama já sabe que acabou).
func (w *webrtcLeg) close() {
	w.closed.Store(true)
	_ = w.pc.Close()
}

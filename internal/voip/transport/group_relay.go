package transport

import (
	"fmt"
	"net"
	"time"

	"github.com/pion/datachannel"
	"github.com/pion/dtls/v3"
	"github.com/pion/dtls/v3/pkg/crypto/selfsign"
	"github.com/pion/logging"
	"github.com/pion/sctp"
)

// Canal de mídia do relay de GRUPO: um DataChannel pré-negociado (id=0) sobre
// SCTP-sobre-DTLS-sobre-UDP, conectado DIRETO ao endpoint do relay (sem ICE/WebRTC —
// diferente do relay 1:1 nosso). A autenticação da mídia é HBH-SRTP, não DTLS, então
// pulamos a verificação do certificado do servidor. Portado de
// github.com/purpshell/meowcaller (MIT) relay/relay.go.

// GroupRelayPacketKind classifica um pacote do canal pelo primeiro byte.
type GroupRelayPacketKind int

const (
	GroupRelayStun GroupRelayPacketKind = iota
	GroupRelayRtcp
	GroupRelayRtp
	GroupRelayOther
)

const (
	groupDataChannelLabel = "pre-negotiated"
)

// ClassifyGroupRelayPacket separa STUN de RTP/RTCP pelo primeiro byte (versão RTP +
// faixa de payload type). RTCP de vídeo do WhatsApp pode começar com 0x91.
func ClassifyGroupRelayPacket(data []byte) GroupRelayPacketKind {
	if len(data) < 2 {
		return GroupRelayOther
	}
	first := data[0]
	if first&0xc0 != 0 {
		if data[1] >= 192 && data[1] <= 223 {
			return GroupRelayRtcp
		}
		if first>>6 == 2 {
			return GroupRelayRtp
		}
		return GroupRelayOther
	}
	return GroupRelayStun
}

// GroupRelayChannel é um canal de mídia de grupo aberto. STUN/RTP/RTCP trafegam como
// mensagens binárias do DataChannel.
type GroupRelayChannel struct {
	udp      net.PacketConn
	dtlsConn net.Conn
	assoc    *sctp.Association
	dc       *datachannel.DataChannel
}

// Close desmonta a pilha na ordem inversa da construção.
func (c *GroupRelayChannel) Close() error {
	if c == nil {
		return nil
	}
	var firstErr error
	for _, closer := range []func() error{
		func() error {
			if c.dc != nil {
				return c.dc.Close()
			}
			return nil
		},
		func() error {
			if c.assoc != nil {
				return c.assoc.Close()
			}
			return nil
		},
		func() error {
			if c.dtlsConn != nil {
				return c.dtlsConn.Close()
			}
			return nil
		},
		func() error {
			if c.udp != nil {
				return c.udp.Close()
			}
			return nil
		},
	} {
		if err := closer(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// Send escreve um pacote (mídia/STUN) como uma mensagem binária do DataChannel.
func (c *GroupRelayChannel) Send(data []byte) (int, error) {
	return c.dc.Write(data)
}

// Recv lê uma mensagem do DataChannel em buf.
func (c *GroupRelayChannel) Recv(buf []byte) (int, error) {
	return c.dc.Read(buf)
}

// BuildGroupBindingSuccess responde a um STUN Binding Request do relay com um Binding
// Success assinado com a chave do grupo (integridade). Devolve ok=false se não for um
// binding request válido.
func BuildGroupBindingSuccess(request, integrityKey []byte) ([]byte, bool) {
	if len(request) < 20 {
		return nil, false
	}
	msgType := uint16(request[0])<<8 | uint16(request[1])
	if msgType != 0x0001 { // STUN Binding Request
		return nil, false
	}
	var txid [12]byte
	copy(txid[:], request[8:20])
	return gEncodeStunRequest(0x0101, txid, nil, integrityKey, true), true // 0x0101 = Binding Success
}

// ConnectGroupRelay conecta a pilha de mídia (UDP→DTLS→SCTP→DataChannel) a UM endpoint
// de relay de grupo. Cert self-signed; verificação do cert do servidor pulada (auth da
// mídia é HBH-SRTP). Só valida contra relay ao vivo.
func ConnectGroupRelay(ip string, port int) (*GroupRelayChannel, error) {
	relayAddr := &net.UDPAddr{IP: net.ParseIP(ip), Port: port}
	if relayAddr.IP == nil {
		return nil, fmt.Errorf("group relay: ip inválido %q", ip)
	}
	var cleanup []func() error
	fail := func(err error) (*GroupRelayChannel, error) {
		for i := len(cleanup) - 1; i >= 0; i-- {
			_ = cleanup[i]()
		}
		return nil, err
	}

	udp, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4zero, Port: 0})
	if err != nil {
		return nil, fmt.Errorf("group relay bind udp: %w", err)
	}
	cleanup = append(cleanup, udp.Close)

	// Deadline cobrindo todo o setup (DTLS + SCTP + datachannel): sem isso, se o relay
	// não responder o handshake fica preso INDEFINIDAMENTE e vaza a goroutine (o
	// relayStarted impede novo retry). Limpamos a deadline no sucesso p/ operação normal.
	_ = udp.SetDeadline(time.Now().Add(12 * time.Second))

	cert, err := selfsign.GenerateSelfSignedWithDNS("wa-voip")
	if err != nil {
		return fail(fmt.Errorf("group relay dtls cert: %w", err))
	}
	dtlsConn, err := dtls.ClientWithOptions(udp, relayAddr,
		dtls.WithCertificates(cert),
		dtls.WithInsecureSkipVerify(true),
	)
	if err != nil {
		return fail(fmt.Errorf("group relay dtls handshake: %w", err))
	}
	cleanup = append(cleanup, dtlsConn.Close)

	assoc, err := sctp.ClientWithOptions(sctp.WithNetConn(dtlsConn), sctp.WithName("wa-voip"))
	if err != nil {
		return fail(fmt.Errorf("group relay sctp: %w", err))
	}
	cleanup = append(cleanup, assoc.Close)

	dc, err := datachannel.Dial(assoc, 0, &datachannel.Config{
		Negotiated:    true,
		Label:         groupDataChannelLabel,
		LoggerFactory: logging.NewDefaultLoggerFactory(),
	})
	if err != nil {
		return fail(fmt.Errorf("group relay datachannel: %w", err))
	}

	// Setup concluído: remove a deadline p/ o tráfego de mídia normal (sem limite).
	_ = udp.SetDeadline(time.Time{})
	return &GroupRelayChannel{udp: udp, dtlsConn: dtlsConn, assoc: assoc, dc: dc}, nil
}

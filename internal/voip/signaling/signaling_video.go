package signaling

import (
	"fmt"

	"wacalls/internal/voip/wanode"

	waBinary "go.mau.fi/whatsmeow/binary"
	"go.mau.fi/whatsmeow/types"
)

// Estados de vídeo mid-call. Depois do offer/accept inicial, o WhatsApp negocia
// ligar/desligar vídeo com stanzas <call><video state=N/></call> independentes.
// Valores observados no WhatsApp Web (paridade com o meowcaller/whatsapp-rust).
const (
	VideoStateDisabled         = 0  // vídeo desligado
	VideoStateEnabled          = 1  // câmera ligada (sem dec)
	VideoStateUpgradeRequest   = 3  // pedido de upgrade (legado)
	VideoStateUpgradeAccept    = 4  // aceita o upgrade (dec = H264,AV1)
	VideoStateUpgradeReject    = 5  // recusa o upgrade
	VideoStateStopped          = 6  // parou/desligou o vídeo (downgrade)
	VideoStateUpgradeCancel    = 8  // cancela o próprio pedido
	VideoStateUpgradeRequestV2 = 11 // pedido de upgrade atual (WhatsApp Web)
)

// Listas de decoder anunciadas na negociação de vídeo.
const (
	VideoDecRequest = "H264"
	VideoDecAccept  = "H264,AV1"
)

// VideoStateParams descreve uma stanza <video state=N> de saída.
type VideoStateParams struct {
	CallID            string
	To                types.JID
	CallCreator       types.JID
	State             int
	Dec               string // incluído se não vazio
	DeviceOrientation *int   // incluído se não nil (0..3)
}

// BuildVideoStateStanza monta o <call><video state=N …/></call> usado para
// sinalizar upgrade/downgrade/on/off de vídeo no meio de uma chamada ativa.
func BuildVideoStateStanza(p VideoStateParams) waBinary.Node {
	attrs := waBinary.Attrs{
		"call-id":      p.CallID,
		"call-creator": p.CallCreator,
		"state":        fmt.Sprintf("%d", p.State),
	}
	if p.Dec != "" {
		attrs["dec"] = p.Dec
	}
	// O pedido de upgrade atual carrega voip_settings=video (o par ignora sem isso).
	if p.State == VideoStateUpgradeRequestV2 {
		attrs["voip_settings"] = "video"
	}
	if p.DeviceOrientation != nil {
		attrs["device_orientation"] = fmt.Sprintf("%d", *p.DeviceOrientation)
	}
	return waBinary.Node{
		Tag:     "call",
		Attrs:   waBinary.Attrs{"to": p.To, "id": GenerateCallStanzaID()},
		Content: []waBinary.Node{{Tag: "video", Attrs: attrs}},
	}
}

// BuildVideoAck monta o ack tipado que toda stanza <video> recebida exige — sem ele
// o WhatsApp interrompe a negociação. Preserva o roteamento p/ dispositivo companion
// (participant/recipient) presente na stanza original.
func BuildVideoAck(original *waBinary.Node) (waBinary.Node, bool) {
	if original == nil {
		return waBinary.Node{}, false
	}
	id := wanode.AttrString(original.Attrs, "id")
	from := wanode.AttrString(original.Attrs, "from")
	if id == "" || from == "" {
		return waBinary.Node{}, false
	}
	attrs := waBinary.Attrs{
		"class": "call", "id": id, "to": wanode.MustJID(from), "type": "video",
	}
	if participant := wanode.AttrString(original.Attrs, "participant"); participant != "" && participant != from {
		attrs["participant"] = wanode.MustJID(participant)
	}
	if recipient := wanode.AttrString(original.Attrs, "recipient"); recipient != "" {
		attrs["recipient"] = wanode.MustJID(recipient)
	}
	return waBinary.Node{Tag: "ack", Attrs: attrs}, true
}

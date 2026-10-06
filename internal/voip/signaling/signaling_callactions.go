package signaling

import (
	"wacalls/internal/voip/wanode"

	waBinary "go.mau.fi/whatsmeow/binary"
	"go.mau.fi/whatsmeow/types"
)

// Ações de chamada mid-call portadas do zapo-js (MIT, github.com/vinikjkkj/zapo):
// mute_v2 (anuncia/observa o microfone) e raise_hand (mão levantada). São STANZAS de
// controle (não mídia), do mesmo formato <call><...>, com ack tipado obrigatório.

const raiseHandAction = "raise_hand"
const raiseHandStateAttr = "raise-hand-state"

// (BuildMuteV2Stanza já existe em signaling_build.go: assinatura
// (peerDeviceJid, callID, callCreator, muteState int). Reaproveitamos aquela.)

// ParseMuteV2 lê o estado anunciado num <mute_v2> recebido. Devolve (muted, isRequest):
// muted=nil quando ausente/desconhecido (nunca chuta); isRequest=true quando a stanza
// traz request-state (um participante PEDINDO que outro mute — mecânica de grupo).
func ParseMuteV2(inner *waBinary.Node) (muted *bool, isRequest bool) {
	if inner == nil {
		return nil, false
	}
	_, isRequest = inner.Attrs["request-state"]
	switch wanode.AttrString(inner.Attrs, "mute-state") {
	case "1":
		t := true
		return &t, isRequest
	case "0":
		f := false
		return &f, isRequest
	}
	return nil, isRequest
}

// BuildRaiseHandStanza monta <call><user_action action=raise_hand ...><raise_hand
// raise-hand-state=0|1/></user_action></call>.
func BuildRaiseHandStanza(to, callCreator types.JID, callID string, raised bool) waBinary.Node {
	state := "0"
	if raised {
		state = "1"
	}
	return waBinary.Node{
		Tag:   "call",
		Attrs: waBinary.Attrs{"to": to, "id": GenerateCallStanzaID()},
		Content: []waBinary.Node{{
			Tag: "user_action",
			Attrs: waBinary.Attrs{
				"call-id": callID, "call-creator": callCreator, "action": raiseHandAction,
			},
			Content: []waBinary.Node{{
				Tag:   raiseHandAction,
				Attrs: waBinary.Attrs{raiseHandStateAttr: state},
			}},
		}},
	}
}

// ParseRaiseHandState lê o estado de mão levantada. Aceita as DUAS formas que o peer
// pode mandar: o envelope <user_action><raise_hand .../> e o <raise_hand> no topo (que
// é um tipo de mensagem próprio). Devolve nil quando ausente (nunca chuta).
func ParseRaiseHandState(inner *waBinary.Node) *bool {
	if inner == nil {
		return nil
	}
	var stateNode *waBinary.Node
	if inner.Tag == raiseHandAction {
		stateNode = inner
	} else {
		for _, c := range wanode.NodeChildren(inner) {
			if c.Tag == raiseHandAction {
				cc := c
				stateNode = &cc
				break
			}
		}
	}
	if stateNode == nil {
		return nil
	}
	switch wanode.AttrString(stateNode.Attrs, raiseHandStateAttr) {
	case "1":
		t := true
		return &t
	case "0":
		f := false
		return &f
	}
	return nil
}

// BuildCallAck monta o ack tipado de uma stanza <call> recebida (type = o tipo do nó
// interno, ex.: "mute_v2", "user_action", "raise_hand"). Sem ack o WhatsApp pode
// interromper a sinalização.
func BuildCallAck(original *waBinary.Node, ackType string) (waBinary.Node, bool) {
	if original == nil {
		return waBinary.Node{}, false
	}
	id := wanode.AttrString(original.Attrs, "id")
	from := wanode.AttrString(original.Attrs, "from")
	if id == "" || from == "" {
		return waBinary.Node{}, false
	}
	attrs := waBinary.Attrs{"class": "call", "id": id, "to": wanode.MustJID(from), "type": ackType}
	if participant := wanode.AttrString(original.Attrs, "participant"); participant != "" && participant != from {
		attrs["participant"] = wanode.MustJID(participant)
	}
	if recipient := wanode.AttrString(original.Attrs, "recipient"); recipient != "" {
		attrs["recipient"] = wanode.MustJID(recipient)
	}
	return waBinary.Node{Tag: "ack", Attrs: attrs}, true
}

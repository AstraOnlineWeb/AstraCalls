package main

import (
	"fmt"
	"strings"

	"wacalls/internal/voip/signaling"

	waBinary "go.mau.fi/whatsmeow/binary"
)

func callIDFromNode(node *waBinary.Node) string {
	info := signaling.ExtractNodeInfo(node)
	if info == nil {
		return ""
	}
	return info.CallID
}

// groupJIDFromOffer devolve o group-jid de um <call><offer> de chamada em GRUPO,
// ou "" se o offer é de uma chamada 1:1 (sem group-jid nem <group_info>).
//
// Quando um número pareado neste gateway é MEMBRO do grupo que outro número está
// chamando, o WhatsApp entrega o offer de grupo ao device vinculado como se fosse
// uma ligação comum. Esse convite NÃO pode entrar no fluxo 1:1: o manager 1:1
// responderia <preaccept>, o SSE anunciaria "incoming" aos integradores (que
// podem atender/recusar via API) e o timeout de toque mandaria <reject> — tudo em
// nome do participante, e o WhatsApp derruba a entrada dele pelo celular (o
// "3º participante entra e sai na hora" relatado pelo AstraChat em 08/10).
func groupJIDFromOffer(node *waBinary.Node) string {
	info := signaling.ExtractNodeInfo(node)
	if info == nil || info.InnerNode == nil || info.InnerNode.Tag != "offer" {
		return ""
	}
	if v, ok := info.InnerNode.Attrs["group-jid"]; ok && v != nil {
		if g := strings.TrimSpace(fmt.Sprint(v)); g != "" {
			return g
		}
	}
	if _, ok := info.InnerNode.GetOptionalChildByTag("group_info"); ok {
		return "(group_info)"
	}
	return ""
}

func isGroupCallOffer(node *waBinary.Node) bool { return groupJIDFromOffer(node) != "" }

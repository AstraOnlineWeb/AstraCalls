package transport

import (
	"crypto/hmac"
	"crypto/sha1"
	"encoding/binary"
	"hash/crc32"
	"slices"
	"strconv"
	"strings"
)

// Builder do ALLOCATE STUN de GRUPO (assinaturas de grupo + HBH-FEC), portado de
// github.com/purpshell/meowcaller (MIT) stun/stun.go. Precisa bater byte-a-byte com
// o servidor do WhatsApp. Tudo aqui é prefixado com `g` para não colidir com o STUN
// 1:1 já existente no pacote (stun.go). Só duas funções são exportadas:
// BuildGroupAllocate e EncodeXorRelayEndpointBytes.

const (
	gStunMagic          = 0x2112a442
	gStunFingerprintXor = 0x5354554e
	gStunXorPort        = 0x2112

	gAttrMessageIntegrity      = 0x0008
	gAttrFingerprint           = 0x8028
	gAttrRelayToken            = 0x4000
	gAttrReceiverSubscriptions = 0x4021
	gAttrStreamDescriptors     = 0x4024
	gAttrWasmRelayEndpoint     = 0x0016
	gAttrSenderSubscriptionsV2 = 0x4025
	gAttrParticipantCount      = 0x805a

	gMsgAllocateRequest uint16 = 0x0003
)

var gStunXorAddr = [4]byte{0x21, 0x12, 0xa4, 0x42}

// BuildGroupAllocate monta o Allocate de grupo (sender subs v2 + receiver subs +
// stream descriptors c/ HBH-FEC + participant count), com MESSAGE-INTEGRITY.
// Quando participantPIDs é vazio, cai no Allocate de stream-ssrcs simples.
func BuildGroupAllocate(
	transactionID [12]byte,
	relayToken []byte,
	endpointXor [6]byte,
	streamSsrcs [9]uint32,
	appDataSSRC uint32,
	hbhFEC [2]uint32,
	participantPIDs []uint32,
	integrityKey []byte,
) []byte {
	pids := gNormalizedParticipantPIDs(participantPIDs)
	if len(pids) == 0 {
		// fallback: só stream descriptors (sem subscrições de grupo)
		attrs := gStunAttr(gAttrRelayToken, relayToken)
		attrs = append(attrs, gStunAttr(gAttrStreamDescriptors, gCreateWasmStreamDescriptors(streamSsrcs))...)
		wep := gCreateWasmRelayEndpointAttr(endpointXor)
		attrs = append(attrs, gStunAttr(gAttrWasmRelayEndpoint, wep[:])...)
		return gEncodeStunRequest(gMsgAllocateRequest, transactionID, attrs, integrityKey, false)
	}
	streamDescriptors := gCreateWasmStreamDescriptors(streamSsrcs)
	if len(pids) > 1 {
		streamDescriptors = gCreateWasmStreamDescriptorsWithHBHFEC(streamSsrcs, hbhFEC)
	}
	attrs := gStunAttr(gAttrRelayToken, relayToken)
	attrs = append(attrs, gStunAttr(gAttrSenderSubscriptionsV2, gCreateWasmGroupSenderSubscriptions(streamSsrcs, appDataSSRC, pids))...)
	attrs = append(attrs, gStunAttr(gAttrReceiverSubscriptions, gCreateWasmGroupReceiverSubscriptions(pids))...)
	attrs = append(attrs, gStunAttr(gAttrStreamDescriptors, streamDescriptors)...)
	participantCount := binary.AppendUvarint(nil, uint64(len(pids)))
	attrs = append(attrs, gStunAttr(gAttrParticipantCount, participantCount)...)
	wep := gCreateWasmRelayEndpointAttr(endpointXor)
	attrs = append(attrs, gStunAttr(gAttrWasmRelayEndpoint, wep[:])...)
	return gEncodeStunRequest(gMsgAllocateRequest, transactionID, attrs, integrityKey, false)
}

// EncodeXorRelayEndpointBytes XOR-encoda um IPv4:port em 6 bytes; ok=false se o
// ipv4 não for 4 octetos.
func EncodeXorRelayEndpointBytes(ipv4 string, port uint16) ([6]byte, bool) {
	var octets []byte
	for _, part := range strings.Split(ipv4, ".") {
		n, err := strconv.ParseUint(part, 10, 8)
		if err != nil {
			continue
		}
		octets = append(octets, byte(n))
	}
	if len(octets) != 4 {
		return [6]byte{}, false
	}
	var buf [6]byte
	binary.BigEndian.PutUint16(buf[0:2], port^gStunXorPort)
	for i := 0; i < 4; i++ {
		buf[2+i] = octets[i] ^ gStunXorAddr[i]
	}
	return buf, true
}

func gPad4(n int) int { return (4 - (n % 4)) % 4 }

func gStunAttr(attrType uint16, value []byte) []byte {
	pad := gPad4(len(value))
	buf := make([]byte, 0, 4+len(value)+pad)
	buf = binary.BigEndian.AppendUint16(buf, attrType)
	buf = binary.BigEndian.AppendUint16(buf, uint16(len(value)))
	buf = append(buf, value...)
	return append(buf, make([]byte, pad)...)
}

func gStunPseudoHeader(msgType, msgLen uint16, transactionID [12]byte) [20]byte {
	var h [20]byte
	binary.BigEndian.PutUint16(h[0:2], msgType)
	binary.BigEndian.PutUint16(h[2:4], msgLen)
	binary.BigEndian.PutUint32(h[4:8], gStunMagic)
	copy(h[8:20], transactionID[:])
	return h
}

func gEncodeStunRequest(msgType uint16, transactionID [12]byte, attrs []byte, integrityKey []byte, includeFingerprint bool) []byte {
	body := append([]byte(nil), attrs...)
	if integrityKey != nil {
		msgLen := uint16(len(body) + 24)
		header := gStunPseudoHeader(msgType, msgLen, transactionID)
		mac := hmac.New(sha1.New, integrityKey)
		mac.Write(header[:])
		mac.Write(body)
		body = append(body, gStunAttr(gAttrMessageIntegrity, mac.Sum(nil))...)
	}
	if includeFingerprint {
		msgLen := uint16(len(body) + 8)
		header := gStunPseudoHeader(msgType, msgLen, transactionID)
		crcInput := make([]byte, 0, 20+len(body))
		crcInput = append(crcInput, header[:]...)
		crcInput = append(crcInput, body...)
		fp := crc32.ChecksumIEEE(crcInput) ^ gStunFingerprintXor
		var fpb [4]byte
		binary.BigEndian.PutUint32(fpb[:], fp)
		body = append(body, gStunAttr(gAttrFingerprint, fpb[:])...)
	}
	out := make([]byte, 0, 20+len(body))
	out = binary.BigEndian.AppendUint16(out, msgType)
	out = binary.BigEndian.AppendUint16(out, uint16(len(body)))
	out = binary.BigEndian.AppendUint32(out, gStunMagic)
	out = append(out, transactionID[:]...)
	return append(out, body...)
}

func gCreateWasmRelayEndpointAttr(endpointXor [6]byte) [8]byte {
	var buf [8]byte
	binary.BigEndian.PutUint16(buf[0:2], 1)
	copy(buf[2:8], endpointXor[:])
	return buf
}

func gNormalizedParticipantPIDs(participantPIDs []uint32) []uint32 {
	pids := append([]uint32(nil), participantPIDs...)
	slices.Sort(pids)
	return slices.Compact(pids)
}

func gPbTag(out []byte, field, wire uint32) []byte {
	return binary.AppendUvarint(out, uint64((field<<3)|wire))
}

func gPbLenDelim(out []byte, field uint32, b []byte) []byte {
	out = gPbTag(out, field, 2)
	out = binary.AppendUvarint(out, uint64(len(b)))
	return append(out, b...)
}

func gCreateWasmGroupSenderSubscriptions(streamSsrcs [9]uint32, appDataSSRC uint32, participantPIDs []uint32) []byte {
	var out []byte
	out = append(out, gCreateWasmSenderSubscription(streamSsrcs[3:6], participantPIDs, true)...)
	out = append(out, gCreateWasmSenderSubscription(streamSsrcs[6:9], nil, false)...)
	out = append(out, gCreateWasmSenderSubscription(streamSsrcs[0:3], participantPIDs, false)...)
	out = append(out, gCreateWasmSenderSubscription([]uint32{appDataSSRC}, participantPIDs, false)...)
	return out
}

func gCreateWasmSenderSubscription(ssrcs []uint32, participantPIDs []uint32, video bool) []byte {
	var packedSSRCs []byte
	for _, ssrc := range ssrcs {
		if ssrc != 0 {
			packedSSRCs = binary.AppendUvarint(packedSSRCs, uint64(ssrc))
		}
	}
	var subscription []byte
	subscription = gPbLenDelim(subscription, 1, packedSSRCs)
	for _, pid := range participantPIDs {
		var participant []byte
		participant = gPbTag(participant, 1, 0)
		participant = binary.AppendUvarint(participant, uint64(pid))
		if video {
			participant = gPbTag(participant, 2, 0)
			participant = binary.AppendUvarint(participant, 1)
		}
		subscription = gPbLenDelim(subscription, 2, participant)
	}
	var wrapper []byte
	wrapper = gPbLenDelim(wrapper, 1, subscription)
	return gPbLenDelim(nil, 1, wrapper)
}

func gCreateWasmGroupReceiverSubscriptions(participantPIDs []uint32) []byte {
	var out []byte
	for _, pid := range participantPIDs {
		var participant []byte
		participant = gPbTag(participant, 1, 0)
		participant = binary.AppendUvarint(participant, uint64(pid))
		out = gPbLenDelim(out, 2, participant)
	}
	return out
}

var gWasmStreamDescriptorPlan = [9]struct{ participant, layer uint32 }{
	{0, 0}, {0, 1}, {0, 2}, {1, 0}, {1, 1}, {1, 2}, {2, 0}, {2, 1}, {2, 2},
}

func gCreateWasmStreamDescriptors(ssrcs [9]uint32) []byte {
	return gCreateWasmStreamDescriptorsWithHBHFEC(ssrcs, [2]uint32{})
}

func gCreateWasmStreamDescriptorsWithHBHFEC(ssrcs [9]uint32, hbhFEC [2]uint32) []byte {
	var out []byte
	for i, ssrc := range ssrcs {
		if ssrc == 0 {
			continue
		}
		plan := gWasmStreamDescriptorPlan[i]
		var inner []byte
		if plan.participant != 0 {
			inner = gPbTag(inner, 1, 0)
			inner = binary.AppendUvarint(inner, uint64(plan.participant))
		}
		if plan.layer != 0 {
			inner = gPbTag(inner, 2, 0)
			inner = binary.AppendUvarint(inner, uint64(plan.layer))
		}
		inner = gPbTag(inner, 3, 0)
		inner = binary.AppendUvarint(inner, uint64(ssrc))
		out = gPbLenDelim(out, 1, inner)
	}
	for i, ssrc := range hbhFEC {
		if ssrc == 0 {
			continue
		}
		var inner []byte
		inner = gPbTag(inner, 1, 0)
		inner = binary.AppendUvarint(inner, uint64(i+3))
		inner = gPbTag(inner, 2, 0)
		inner = binary.AppendUvarint(inner, 3)
		inner = gPbTag(inner, 3, 0)
		inner = binary.AppendUvarint(inner, uint64(ssrc))
		out = gPbLenDelim(out, 1, inner)
	}
	return out
}

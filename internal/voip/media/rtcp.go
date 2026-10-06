package media

import (
	"encoding/binary"
)

// RTCP do WhatsApp: Sender Report (PT 200) + SDES (PT 202) compostos, e o PLI (PT 206,
// FMT 1) de pedido de keyframe. Portado de github.com/purpshell/meowcaller (MIT)
// rtp/rtcp.go — bate byte-a-byte com os vetores do meowcaller (ver rtcp_test.go).
//
// Por que existe: na chamada em GRUPO os celulares só mandam keyframe de vídeo SOB
// PEDIDO (PLI) e associam os streams RTP a uma sessão SRTCP (os SR+SDES periódicos).
// Sem isso o vídeo dos participantes não abre de forma confiável e o nosso pode
// ficar "conectando" no aparelho deles.

const (
	RtcpPtSr   uint8 = 200
	RtcpPtSdes uint8 = 202
	RtcpPtPsfb uint8 = 206

	rtcpHeaderLen        = 8
	ntpUnixOffsetSecs    = 2208988800
	WhatsappRtcpCnameLen = 18
)

// RtcpSenderStats são os contadores do Sender Report.
type RtcpSenderStats struct {
	PacketsSent  uint32
	OctetsSent   uint32
	RtpTimestamp uint32
}

// IsRtcpPacket diz se data é um pacote RTCP (versão 2, PT 192..223).
func IsRtcpPacket(data []byte) bool {
	if len(data) < rtcpHeaderLen {
		return false
	}
	if (data[0]>>6)&0x03 != 2 {
		return false
	}
	return data[1] >= 192 && data[1] <= 223
}

// ParseRtcpSenderSsrc devolve o SSRC do emissor (bytes 4..7).
func ParseRtcpSenderSsrc(data []byte) (uint32, bool) {
	if len(data) < 8 || (data[0]>>6)&0x03 != 2 {
		return 0, false
	}
	return binary.BigEndian.Uint32(data[4:8]), true
}

// BuildSenderReport monta o SR de 28 bytes (PT 200, RC=0); nowMs é relógio de parede.
func BuildSenderReport(localSsrc uint32, stats *RtcpSenderStats, nowMs uint64) [28]byte {
	var buf [28]byte
	buf[0] = 0x80 // V=2, RC=0
	buf[1] = RtcpPtSr
	buf[3] = 6 // (6+1)*4 = 28 bytes
	binary.BigEndian.PutUint32(buf[4:8], localSsrc)
	ntpSec := uint32((nowMs / 1000) + ntpUnixOffsetSecs)
	ntpFrac := uint32(float64(nowMs%1000) / 1000.0 * 4294967296.0)
	binary.BigEndian.PutUint32(buf[8:12], ntpSec)
	binary.BigEndian.PutUint32(buf[12:16], ntpFrac)
	if stats != nil {
		binary.BigEndian.PutUint32(buf[16:20], stats.RtpTimestamp)
		binary.BigEndian.PutUint32(buf[20:24], stats.PacketsSent)
		binary.BigEndian.PutUint32(buf[24:28], stats.OctetsSent)
	}
	return buf
}

// BuildWhatsappRtcpCname monta o CNAME de 18 bytes no formato nativo ("xxxxx@pjxxxxxx.org").
func BuildWhatsappRtcpCname(entropy [12]byte) [WhatsappRtcpCnameLen]byte {
	const hexChars = "0123456789abcdef"
	var randomHex [11]byte
	for nibble := range randomHex {
		b := entropy[6+nibble/2]
		if nibble&1 == 0 {
			randomHex[nibble] = hexChars[b>>4]
		} else {
			randomHex[nibble] = hexChars[b&0x0f]
		}
	}
	var cname [WhatsappRtcpCnameLen]byte
	copy(cname[:5], randomHex[:5])
	copy(cname[5:8], "@pj")
	copy(cname[8:14], randomHex[5:])
	copy(cname[14:], ".org")
	return cname
}

// BuildSourceDescription monta o SDES de um chunk (32 bytes) do WhatsApp.
func BuildSourceDescription(localSsrc uint32, cname *[WhatsappRtcpCnameLen]byte, profileExtension bool) [32]byte {
	var packet [32]byte
	packet[0] = 0x81
	if profileExtension {
		packet[0] |= 0x10
	}
	packet[1] = RtcpPtSdes
	binary.BigEndian.PutUint16(packet[2:4], 7)
	binary.BigEndian.PutUint32(packet[4:8], localSsrc)
	packet[8] = 1
	packet[9] = WhatsappRtcpCnameLen
	copy(packet[10:28], cname[:])
	return packet
}

// BuildSenderReportWithSdes monta o composto periódico SR+SDES do WhatsApp.
// profileExtension liga o bit de perfil (usado no stream de VÍDEO).
func BuildSenderReportWithSdes(localSsrc uint32, stats *RtcpSenderStats, nowMs uint64, cname *[WhatsappRtcpCnameLen]byte, profileExtension bool) []byte {
	sr := BuildSenderReport(localSsrc, stats, nowMs)
	if profileExtension {
		sr[0] |= 0x10
	}
	sdes := BuildSourceDescription(localSsrc, cname, profileExtension)
	out := make([]byte, 0, len(sr)+len(sdes))
	out = append(out, sr[:]...)
	out = append(out, sdes[:]...)
	return out
}

// BuildPictureLossIndication monta um PLI (RFC 4585) pedindo um keyframe ao emissor
// de mediaSSRC.
func BuildPictureLossIndication(senderSSRC, mediaSSRC uint32, profileExtension bool) [12]byte {
	var packet [12]byte
	packet[0] = 0x81
	if profileExtension {
		packet[0] |= 0x10
	}
	packet[1] = RtcpPtPsfb
	binary.BigEndian.PutUint16(packet[2:4], 2)
	binary.BigEndian.PutUint32(packet[4:8], senderSSRC)
	binary.BigEndian.PutUint32(packet[8:12], mediaSSRC)
	return packet
}

// RtcpRequestsKeyframe diz se um RTCP composto (já decifrado) contém PLI/FIR
// endereçado a localVideoSsrc.
func RtcpRequestsKeyframe(data []byte, localVideoSsrc uint32) bool {
	for offset := 0; offset+4 <= len(data); {
		if (data[offset]>>6)&0x03 != 2 {
			return false
		}
		packetLen := (int(binary.BigEndian.Uint16(data[offset+2:offset+4])) + 1) * 4
		if packetLen < 8 || offset+packetLen > len(data) {
			return false
		}
		packet := data[offset : offset+packetLen]
		if packet[1] == RtcpPtPsfb && len(packet) >= 12 {
			rawFmt := packet[0] & 0x1f
			fmt := rawFmt
			if rawFmt&0x10 != 0 {
				fmt &= 0x0f
			}
			mediaSsrc := binary.BigEndian.Uint32(packet[8:12])
			if fmt == 1 && mediaSsrc == localVideoSsrc {
				return true
			}
			if fmt == 4 {
				if mediaSsrc == localVideoSsrc {
					return true
				}
				for fci := packet[12:]; len(fci) >= 8; fci = fci[8:] {
					if binary.BigEndian.Uint32(fci[:4]) == localVideoSsrc {
						return true
					}
				}
			}
		}
		offset += packetLen
	}
	return false
}

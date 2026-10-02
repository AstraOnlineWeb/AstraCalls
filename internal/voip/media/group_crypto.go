package media

import (
	"crypto/hkdf"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"strings"

	"wacalls/internal/voip/core"
)

// Derivação de chave/SSRC para chamada em GRUPO (keygen-v2). Precisa casar
// byte-a-byte com o servidor do WhatsApp, então segue a referência do meowcaller
// (MIT) / whatsapp-rust exatamente:
//
//   - participantID = FormatParticipantID(deviceJID): tira o recurso e dá :0 a um
//     @lid sem device. É o `info` tanto da chave SRTP quanto do SSRC.
//   - chave SRTP = HKDF-SHA256(salt=zeros[32], ikm=epoch[:32], info=participantID, 46)
//     -> MasterKey(16)+MasterSalt(14), que a nossa SRTP (NewSrtpContext) consome
//     igual ao 1:1 (mesmo AES-CM KDF e IV). Reaproveitamos a SRTP existente.
//   - SSRC = HKDF-SHA256(salt=slotWord LE32, ikm=callID, info=participantID, 4) lido
//     como u32 little-endian. São 9 streams por participante (áudio=slot 0).

// WasmRelayStreamSlotWords são os slots dos 9 streams do plano de allocate do relay.
var WasmRelayStreamSlotWords = [9]uint32{0, 1, 4, 2, 3, 5, 7, 8, 6}

const (
	// Slots de SSRC por participante (áudio é o slot 0).
	GroupVideoSlotWord   uint32 = 2
	GroupAppDataSlotWord uint32 = 6
	GroupHBHFECTXSlot    uint32 = 7
	GroupHBHFECRXSlot    uint32 = 8
)

var errShortEpoch = errors.New("group epoch key shorter than 32 bytes")

// FormatParticipantID formata o LID com device p/ usar como info do HKDF (SRTP/SSRC):
// tira o /recurso e dá ":0" a um "<user>@lid" sem device; o resto passa igual.
func FormatParticipantID(jid string) string {
	bare, _, _ := strings.Cut(jid, "/")
	bare = strings.TrimSpace(bare)
	at := strings.LastIndexByte(bare, '@')
	if at <= 0 {
		return bare
	}
	user := bare[:at]
	domain := bare[at+1:]
	if domain == "lid" && !strings.Contains(user, ":") {
		return user + ":0@" + domain
	}
	return bare
}

// DeriveGroupSrtpKeying deriva o material SRTP (MasterKey+MasterSalt) de um
// participante a partir da chave de epoch da call. participantID já deve vir de
// FormatParticipantID. Alimenta NewSrtpContext como no 1:1.
func DeriveGroupSrtpKeying(epochKey []byte, participantID string) (core.SrtpKeyingMaterial, error) {
	if len(epochKey) < 32 {
		return core.SrtpKeyingMaterial{}, errShortEpoch
	}
	// nil salt == zeros[32] no hkdf do Go (hashlen=32) == salt explícito do meowcaller.
	return DerivePerJidSrtpKey(epochKey[:32], participantID)
}

// DeriveParticipantSSRC deriva um SSRC de stream: HKDF(salt=slotWord LE32,
// ikm=callID, info=participantID, 4) lido como u32 little-endian.
func DeriveParticipantSSRC(callID, participantID string, slotWord uint32) (uint32, error) {
	salt := binary.LittleEndian.AppendUint32(nil, slotWord)
	okm, err := hkdf.Key(sha256.New, []byte(callID), salt, participantID, 4)
	if err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint32(okm), nil
}

// DeriveRelayStreamSSRCs deriva os 9 SSRCs de stream de um participante, na ordem
// dos slots. O índice 0 é o áudio.
func DeriveRelayStreamSSRCs(callID, participantID string) ([9]uint32, error) {
	var out [9]uint32
	for i, slot := range WasmRelayStreamSlotWords {
		s, err := DeriveParticipantSSRC(callID, participantID, slot)
		if err != nil {
			return [9]uint32{}, err
		}
		out[i] = s
	}
	return out, nil
}

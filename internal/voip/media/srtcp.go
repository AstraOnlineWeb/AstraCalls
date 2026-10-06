package media

import (
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/binary"
	"sync"
)

// SRTCP end-to-end do WhatsApp (chamada em GRUPO): mesmas chaves-mestras por
// participante da SRTP (HKDF do epoch com info=participantID) mas expandidas com os
// labels SRTCP da RFC 3711 (0x03 cifra, 0x04 auth, 0x05 salt). Portado de
// github.com/purpshell/meowcaller (MIT) srtp/e2e.go.

const (
	SrtcpAuthTagLen = 10
	SrtcpTrailerLen = 4 + SrtcpAuthTagLen
)

// SrtcpKeys são as chaves de sessão SRTCP de um participante.
type SrtcpKeys struct {
	CipherKey [16]byte
	Salt      [14]byte
	AuthKey   [20]byte
}

// DeriveGroupSrtcpKeys deriva as chaves SRTCP de um participante a partir da chave de
// epoch (32 bytes) e do participantID (FormatParticipantID).
func DeriveGroupSrtcpKeys(epochKey []byte, participantID string) (SrtcpKeys, error) {
	if len(epochKey) < 32 {
		return SrtcpKeys{}, errShortEpoch
	}
	master, err := hkdf.Key(sha256.New, epochKey[:32], nil, participantID, 46)
	if err != nil {
		return SrtcpKeys{}, err
	}
	masterKey, masterSalt := master[0:16], master[16:30]
	var keys SrtcpKeys
	ck, err := deriveSrtpKey(masterKey, masterSalt, 0x03, 16)
	if err != nil {
		return SrtcpKeys{}, err
	}
	ak, err := deriveSrtpKey(masterKey, masterSalt, 0x04, 20)
	if err != nil {
		return SrtcpKeys{}, err
	}
	salt, err := deriveSrtpKey(masterKey, masterSalt, 0x05, 14)
	if err != nil {
		return SrtcpKeys{}, err
	}
	copy(keys.CipherKey[:], ck)
	copy(keys.AuthKey[:], ak)
	copy(keys.Salt[:], salt)
	return keys, nil
}

// srtcpIV monta o IV AES-CTR: salt alinhado à esquerda, SSRC XOR nos bytes 4..7 e o
// índice de 48 bits (ROC<<16 | seq) XOR nos bytes 8..13 — igual ao RTP.
func srtcpIV(keys *SrtcpKeys, ssrc uint32, index uint32) []byte {
	iv := make([]byte, 16)
	copy(iv, keys.Salt[:])
	var ssrcBuf [4]byte
	binary.BigEndian.PutUint32(ssrcBuf[:], ssrc)
	for i := 0; i < 4; i++ {
		iv[4+i] ^= ssrcBuf[i]
	}
	packetIndex := (uint64(index>>16) << 16) | uint64(uint16(index))
	var idxBuf [8]byte
	binary.BigEndian.PutUint64(idxBuf[:], packetIndex)
	for i := 0; i < 6; i++ {
		iv[8+i] ^= idxBuf[2+i]
	}
	return iv
}

// ProtectSrtcp cifra e autentica um pacote RTCP (cabeçalho de 8 bytes em claro, corpo
// cifrado, E-bit|index de 4 bytes e tag HMAC-SHA1 de 10 bytes).
func ProtectSrtcp(keys *SrtcpKeys, senderSsrc, index uint32, rtcp []byte) ([]byte, error) {
	split := len(rtcp)
	if split > rtcpHeaderLen {
		split = rtcpHeaderLen
	}
	out := append([]byte(nil), rtcp[:split]...)
	body := make([]byte, len(rtcp)-split)
	if err := aesCtrXor(keys.CipherKey[:], srtcpIV(keys, senderSsrc, index), rtcp[split:], body); err != nil {
		return nil, err
	}
	out = append(out, body...)
	out = binary.BigEndian.AppendUint32(out, 0x80000000|index)
	mac := hmac.New(sha1.New, keys.AuthKey[:])
	_, _ = mac.Write(out)
	out = append(out, mac.Sum(nil)[:SrtcpAuthTagLen]...)
	return out, nil
}

// UnprotectSrtcp autentica e decifra um pacote SRTCP. Devolve o RTCP em claro e o índice.
func UnprotectSrtcp(keys *SrtcpKeys, senderSsrc uint32, packet []byte) ([]byte, uint32, bool) {
	if len(packet) < rtcpHeaderLen+SrtcpTrailerLen {
		return nil, 0, false
	}
	tagStart := len(packet) - SrtcpAuthTagLen
	mac := hmac.New(sha1.New, keys.AuthKey[:])
	_, _ = mac.Write(packet[:tagStart])
	if !hmac.Equal(packet[tagStart:], mac.Sum(nil)[:SrtcpAuthTagLen]) {
		return nil, 0, false
	}
	indexStart := tagStart - 4
	index := binary.BigEndian.Uint32(packet[indexStart:tagStart]) & 0x7fffffff
	body := make([]byte, indexStart-rtcpHeaderLen)
	if err := aesCtrXor(keys.CipherKey[:], srtcpIV(keys, senderSsrc, index), packet[rtcpHeaderLen:indexStart], body); err != nil {
		return nil, 0, false
	}
	out := append([]byte(nil), packet[:rtcpHeaderLen]...)
	out = append(out, body...)
	return out, index, true
}

// SrtcpSender emite RTCP protegido (SR+SDES periódicos e PLI) em nome de UM stream
// nosso (áudio ou vídeo). Thread-safe.
type SrtcpSender struct {
	mu      sync.Mutex
	keys    SrtcpKeys
	ssrc    uint32
	cname   [WhatsappRtcpCnameLen]byte
	profile bool
	index   uint32
}

// NewSrtcpSender cria o emissor do stream ssrc com chaves do (epoch, selfID).
// profile=true liga o bit de perfil nos pacotes (stream de vídeo).
func NewSrtcpSender(epochKey []byte, selfID string, ssrc uint32, profile bool) (*SrtcpSender, error) {
	keys, err := DeriveGroupSrtcpKeys(epochKey, selfID)
	if err != nil {
		return nil, err
	}
	var entropy [12]byte
	_, _ = rand.Read(entropy[:])
	return &SrtcpSender{keys: keys, ssrc: ssrc, cname: BuildWhatsappRtcpCname(entropy), profile: profile, index: 1}, nil
}

// Rekey troca as chaves pro epoch novo mantendo índice e CNAME (rotação de epoch).
func (s *SrtcpSender) Rekey(epochKey []byte, selfID string) error {
	keys, err := DeriveGroupSrtcpKeys(epochKey, selfID)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.keys = keys
	s.mu.Unlock()
	return nil
}

// SSRC devolve o SSRC do stream que este emissor representa.
func (s *SrtcpSender) SSRC() uint32 { return s.ssrc }

// SenderReport monta e protege o composto SR+SDES.
func (s *SrtcpSender) SenderReport(stats RtcpSenderStats, nowMs uint64) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	plain := BuildSenderReportWithSdes(s.ssrc, &stats, nowMs, &s.cname, s.profile)
	packet, err := ProtectSrtcp(&s.keys, s.ssrc, s.index, plain)
	if err == nil {
		s.index++
	}
	return packet, err
}

// PictureLossIndication monta e protege um PLI pedindo keyframe ao emissor de mediaSSRC.
func (s *SrtcpSender) PictureLossIndication(mediaSSRC uint32) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	plain := BuildPictureLossIndication(s.ssrc, mediaSSRC, s.profile)
	packet, err := ProtectSrtcp(&s.keys, s.ssrc, s.index, plain[:])
	if err == nil {
		s.index++
	}
	return packet, err
}

// Keying devolve as chaves (p/ testes).
func (s *SrtcpSender) Keying() SrtcpKeys { return s.keys }

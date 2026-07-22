package media

import (
	"crypto/hmac"
	"crypto/sha1"
	"encoding/binary"

	"wacalls/internal/voip/core"
)

// Labels de derivação de chave SRTCP (RFC 3711): 3=enc, 4=auth, 5=salt.
const (
	srtcpLabelEncryption = 0x03
	srtcpLabelAuth       = 0x04
	srtcpLabelSalt       = 0x05
)

// SrtcpContext protege pacotes RTCP (gera SRTCP) usando a mesma keying material
// do SRTP, porém com os labels SRTCP. Usado para enviar Sender Reports do nosso
// stream de vídeo — o renderizador de vídeo do peer exige o SR para exibir.
type SrtcpContext struct {
	sessionKey  []byte
	sessionSalt []byte
	authKey     []byte
	authTagLen  int
	index       uint32
}

func NewSrtcpContext(keying core.SrtpKeyingMaterial, authTagLen int) (*SrtcpContext, error) {
	if authTagLen <= 0 {
		authTagLen = core.SRTPAuthTagLen
	}
	sk, err := deriveSrtpKey(keying.MasterKey, keying.MasterSalt, srtcpLabelEncryption, 16)
	if err != nil {
		return nil, err
	}
	ak, err := deriveSrtpKey(keying.MasterKey, keying.MasterSalt, srtcpLabelAuth, 20)
	if err != nil {
		return nil, err
	}
	ss, err := deriveSrtpKey(keying.MasterKey, keying.MasterSalt, srtcpLabelSalt, 14)
	if err != nil {
		return nil, err
	}
	return &SrtcpContext{sessionKey: sk, sessionSalt: ss, authKey: ak, authTagLen: authTagLen}, nil
}

// Protect recebe um pacote RTCP em claro e devolve o SRTCP correspondente:
// [primeiros 8 bytes em claro][resto cifrado AES-CM][E|index (4B)][auth tag].
func (c *SrtcpContext) Protect(rtcp []byte) []byte {
	if len(rtcp) < 8 {
		return nil
	}
	c.index = (c.index + 1) & 0x7fffffff
	idx := c.index
	ssrc := binary.BigEndian.Uint32(rtcp[4:8])

	out := make([]byte, len(rtcp))
	copy(out[:8], rtcp[:8])
	iv := c.generateIV(ssrc, idx)
	if err := aesCtrXor(c.sessionKey, iv, rtcp[8:], out[8:]); err != nil {
		return nil
	}

	// E=1 (cifrado) + index de 31 bits
	var eidx [4]byte
	binary.BigEndian.PutUint32(eidx[:], idx|0x80000000)
	out = append(out, eidx[:]...)

	// auth tag = HMAC-SHA1(authKey, out) truncado
	mac := hmac.New(sha1.New, c.authKey)
	mac.Write(out)
	out = append(out, mac.Sum(nil)[:c.authTagLen]...)
	return out
}

// VerifyAuth recomputa o auth tag de um pacote SRTCP recebido e diz se confere.
// Usado só para validar que nossa derivação/algoritmo SRTCP batem com o WhatsApp.
func (c *SrtcpContext) VerifyAuth(srtcp []byte) bool {
	if len(srtcp) < c.authTagLen+12 {
		return false
	}
	body := srtcp[:len(srtcp)-c.authTagLen]
	tag := srtcp[len(srtcp)-c.authTagLen:]
	mac := hmac.New(sha1.New, c.authKey)
	mac.Write(body)
	return hmac.Equal(mac.Sum(nil)[:c.authTagLen], tag)
}

// Unprotect descriptografa um SRTCP recebido (sem checar auth) e devolve o RTCP
// em claro + o índice. Usado para validar que nosso IV/cripto batem com o WhatsApp.
func (c *SrtcpContext) Unprotect(srtcp []byte) (rtcp []byte, index uint32, ok bool) {
	if len(srtcp) < c.authTagLen+12 {
		return nil, 0, false
	}
	body := srtcp[:len(srtcp)-c.authTagLen] // remove auth tag
	if len(body) < 12 {
		return nil, 0, false
	}
	eidx := binary.BigEndian.Uint32(body[len(body)-4:])
	index = eidx & 0x7fffffff
	enc := body[:len(body)-4] // RTCP cifrado (8 bytes claros + resto cifrado)
	ssrc := binary.BigEndian.Uint32(enc[4:8])
	out := make([]byte, len(enc))
	copy(out[:8], enc[:8])
	iv := c.generateIV(ssrc, index)
	if err := aesCtrXor(c.sessionKey, iv, enc[8:], out[8:]); err != nil {
		return nil, 0, false
	}
	return out, index, true
}

// generateIV: SRTCP IV = salt(112b)<<16 XOR SSRC<<64 XOR index<<16 (RFC 3711).
func (c *SrtcpContext) generateIV(ssrc, index uint32) []byte {
	iv := make([]byte, 16)
	copy(iv, c.sessionSalt[:14])
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], ssrc)
	for i := 0; i < 4; i++ {
		iv[4+i] ^= b[i]
	}
	binary.BigEndian.PutUint32(b[:], index)
	for i := 0; i < 4; i++ {
		iv[10+i] ^= b[i]
	}
	return iv
}

// BuildSenderReport monta um RTCP Sender Report (PT=200) em claro.
// ntpSec/ntpFrac são o timestamp NTP (segundos desde 1900); rtpTs é o timestamp
// RTP corrente do stream; packets/octets são os contadores enviados.
func BuildSenderReport(ssrc uint32, ntpSec, ntpFrac uint32, rtpTs uint32, packets, octets uint32) []byte {
	buf := make([]byte, 28)
	buf[0] = 0x80 // V=2, P=0, RC=0
	buf[1] = 200  // PT = SR
	binary.BigEndian.PutUint16(buf[2:], 6) // length em words-1 = (28/4)-1 = 6
	binary.BigEndian.PutUint32(buf[4:], ssrc)
	binary.BigEndian.PutUint32(buf[8:], ntpSec)
	binary.BigEndian.PutUint32(buf[12:], ntpFrac)
	binary.BigEndian.PutUint32(buf[16:], rtpTs)
	binary.BigEndian.PutUint32(buf[20:], packets)
	binary.BigEndian.PutUint32(buf[24:], octets)
	return buf
}

// BuildSDES monta um RTCP SDES (PT=202) com um chunk (ssrc + item CNAME).
// O CNAME liga o SSRC a uma fonte canônica; muitos SFUs só associam/renderizam
// o stream depois de receber o SDES.
func BuildSDES(ssrc uint32, cname string) []byte {
	cn := []byte(cname)
	// chunk = ssrc(4) + item[type=1, len, cname] + item end(type=0) + padding p/ múltiplo de 4
	chunk := make([]byte, 0, 4+2+len(cn)+1)
	var ssrcb [4]byte
	binary.BigEndian.PutUint32(ssrcb[:], ssrc)
	chunk = append(chunk, ssrcb[:]...)
	chunk = append(chunk, 0x01, byte(len(cn))) // item type 1 = CNAME, length
	chunk = append(chunk, cn...)
	chunk = append(chunk, 0x00) // item type 0 = fim da lista
	for len(chunk)%4 != 0 {
		chunk = append(chunk, 0x00)
	}
	buf := make([]byte, 4+len(chunk))
	buf[0] = 0x81 // V=2, P=0, SC=1 (1 chunk)
	buf[1] = 202  // PT = SDES
	binary.BigEndian.PutUint16(buf[2:], uint16((len(buf)/4)-1))
	copy(buf[4:], chunk)
	return buf
}

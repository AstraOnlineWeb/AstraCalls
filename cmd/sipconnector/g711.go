package main

import "encoding/binary"

// G.711 (PCMU = payload 0, PCMA = payload 8) e conversão de taxa 8k <-> 16k.
// O navegador sempre fala PCMU com o conector (é o único codec que registramos no
// WebRTC); se o PABX negociar PCMA, convertemos byte a byte. Para a IA, o áudio
// vira PCM16 16 kHz (o contrato pcm16 do wacalls).
const (
	ptPCMU uint8 = 0
	ptPCMA uint8 = 8
)

func ulawToLinear(u byte) int16 {
	u = ^u
	sign := u & 0x80
	exponent := (u & 0x70) >> 4
	sample := (int16(u&0x0F) << 3) + 0x84
	sample <<= exponent
	sample -= 0x84
	if sign != 0 {
		return -sample
	}
	return sample
}

func linearToUlaw(sample int16) byte {
	const bias = 0x84
	const clip = 32635
	sign := byte(0)
	s := int32(sample)
	if s < 0 {
		s = -s
		sign = 0x80
	}
	if s > clip {
		s = clip
	}
	s += bias
	exponent := byte(7)
	for mask := int32(0x4000); s&mask == 0 && exponent > 0; mask >>= 1 {
		exponent--
	}
	mantissa := byte((s >> (exponent + 3)) & 0x0F)
	return ^(sign | exponent<<4 | mantissa)
}

func alawToLinear(a byte) int16 {
	a ^= 0x55
	sign := a & 0x80
	exponent := (a & 0x70) >> 4
	data := int16(a&0x0F)<<4 + 8
	if exponent != 0 {
		data = (data + 0x100) << (exponent - 1)
	}
	if sign == 0 {
		return -data
	}
	return data
}

// linearToAlaw segue o algoritmo de referência (g711.c da Sun) sobre 13 bits.
var alawSegEnd = [8]int32{0x1F, 0x3F, 0x7F, 0xFF, 0x1FF, 0x3FF, 0x7FF, 0xFFF}

func linearToAlaw(sample int16) byte {
	pcm := int32(sample) >> 3
	mask := byte(0xD5)
	if pcm < 0 {
		mask = 0x55
		pcm = -pcm - 1
	}
	seg := 0
	for seg < 8 && pcm > alawSegEnd[seg] {
		seg++
	}
	if seg >= 8 {
		return 0x7F ^ mask
	}
	aval := byte(seg << 4)
	if seg < 2 {
		aval |= byte((pcm >> 1) & 0x0F)
	} else {
		aval |= byte((pcm >> uint(seg)) & 0x0F)
	}
	return aval ^ mask
}

var (
	ulawDecode [256]int16
	alawDecode [256]int16
	alawToUlaw [256]byte
)

func init() {
	for i := 0; i < 256; i++ {
		ulawDecode[i] = ulawToLinear(byte(i))
		alawDecode[i] = alawToLinear(byte(i))
		alawToUlaw[i] = linearToUlaw(alawDecode[i])
	}
}

// decodeG711 devolve as amostras 8 kHz do payload.
func decodeG711(payload []byte, pt uint8) []int16 {
	table := &ulawDecode
	if pt == ptPCMA {
		table = &alawDecode
	}
	out := make([]int16, len(payload))
	for i, b := range payload {
		out[i] = table[b]
	}
	return out
}

func encodeG711(samples []int16, pt uint8) []byte {
	out := make([]byte, len(samples))
	for i, s := range samples {
		if pt == ptPCMA {
			out[i] = linearToAlaw(s)
		} else {
			out[i] = linearToUlaw(s)
		}
	}
	return out
}

// toPCMU converte o payload do PABX para o PCMU do navegador.
func toPCMU(payload []byte, pt uint8) []byte {
	if pt != ptPCMA {
		return payload
	}
	out := make([]byte, len(payload))
	for i, b := range payload {
		out[i] = alawToUlaw[b]
	}
	return out
}

// fromPCMU converte o PCMU do navegador para o codec negociado com o PABX.
func fromPCMU(payload []byte, pt uint8) []byte {
	if pt != ptPCMA {
		return payload
	}
	out := make([]byte, len(payload))
	for i, b := range payload {
		out[i] = linearToAlaw(ulawDecode[b])
	}
	return out
}

// upsample8to16 dobra a taxa por interpolação linear. `prev` é a última amostra do
// quadro anterior, para não haver degrau entre quadros.
func upsample8to16(in []int16, prev int16) []int16 {
	out := make([]int16, len(in)*2)
	for i, s := range in {
		out[2*i] = int16((int32(prev) + int32(s)) / 2)
		out[2*i+1] = s
		prev = s
	}
	return out
}

// downsample16to8 tira a média de cada par (filtro passa-baixa mínimo antes de dizimar).
func downsample16to8(in []int16) []int16 {
	out := make([]int16, len(in)/2)
	for i := range out {
		out[i] = int16((int32(in[2*i]) + int32(in[2*i+1])) / 2)
	}
	return out
}

func int16ToBytes(s []int16) []byte {
	out := make([]byte, len(s)*2)
	for i, v := range s {
		binary.LittleEndian.PutUint16(out[2*i:], uint16(v))
	}
	return out
}

func bytesToInt16(b []byte) []int16 {
	out := make([]int16, len(b)/2)
	for i := range out {
		out[i] = int16(binary.LittleEndian.Uint16(b[2*i:]))
	}
	return out
}

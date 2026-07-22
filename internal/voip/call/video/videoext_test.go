package video

import "testing"

// O primeiro pacote de um access unit leva FrameNumber (id3 len3 = 0x32) e os
// blocos id5/id6/id9; o total é padded a múltiplo de 4 (16 bytes p/ este caso).
func TestVideoExtPrimeiroPacote(t *testing.T) {
	fn := uint16(1)
	ext := buildWhatsappVideoExt(videoMediaFrameIDR, &fn, 5)
	if len(ext)%4 != 0 {
		t.Fatalf("extensão precisa ser múltiplo de 4 bytes, veio %d", len(ext))
	}
	want := []byte{
		0x32, 0x08, 0x00, 0x01, // id3 len3: MediaFrameInfo=IDR, FrameNumber=1
		0x51, 0x00, 0x00, // id5 InitialBandwidth=0
		0x61, 0x00, 0x00, // id6 ShortOffset=0
		0x91, 0x00, 0x05, // id9 TransportSequence=5
		0x00, 0x00, 0x00, // padding até 16
	}
	if len(ext) != len(want) {
		t.Fatalf("tamanho = %d, quer %d (%x)", len(ext), len(want), ext)
	}
	for i := range want {
		if ext[i] != want[i] {
			t.Fatalf("byte %d = 0x%02x, quer 0x%02x (ext=%x)", i, ext[i], want[i], ext)
		}
	}
}

// Pacotes seguintes do mesmo access unit NÃO levam FrameNumber (id3 len1 = 0x30).
func TestVideoExtPacoteSeguinte(t *testing.T) {
	ext := buildWhatsappVideoExt(videoMediaFrameDelta, nil, 9)
	if len(ext)%4 != 0 {
		t.Fatalf("extensão precisa ser múltiplo de 4, veio %d", len(ext))
	}
	if ext[0] != 0x30 || ext[1] != videoMediaFrameDelta {
		t.Fatalf("cabeçalho id3 errado: %x", ext[:2])
	}
	// id9 (TransportSequence) deve estar presente com o valor certo.
	if ext[8] != 0x91 || ext[9] != 0x00 || ext[10] != 0x09 {
		t.Fatalf("bloco id9 errado: %x", ext)
	}
}

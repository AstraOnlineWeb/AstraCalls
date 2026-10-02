package media

import (
	"bytes"
	"testing"
)

func TestSplitContainer(t *testing.T) {
	// Container 0x92 válido: count=2, 1º sub-frame de 3 bytes, 2º = resto.
	p := []byte{0x92, 0x02, 0x03, 0xAA, 0xBB, 0xCC, 0xDD, 0xEE}
	subs, ok := splitContainer(p)
	if !ok {
		t.Fatalf("container válido rejeitado")
	}
	if len(subs) != 2 {
		t.Fatalf("subs = %d, quer 2", len(subs))
	}
	if !bytes.Equal(subs[0], []byte{0xAA, 0xBB, 0xCC}) {
		t.Errorf("sub[0] = %x", subs[0])
	}
	if !bytes.Equal(subs[1], []byte{0xDD, 0xEE}) {
		t.Errorf("sub[1] = %x", subs[1])
	}

	// Container de 3 frames.
	p3 := []byte{0x92, 0x03, 0x02, 0x11, 0x22, 0x01, 0x33, 0x44, 0x55}
	subs3, ok := splitContainer(p3)
	if !ok || len(subs3) != 3 {
		t.Fatalf("container de 3: ok=%v len=%d", ok, len(subs3))
	}
	if !bytes.Equal(subs3[0], []byte{0x11, 0x22}) || !bytes.Equal(subs3[1], []byte{0x33}) || !bytes.Equal(subs3[2], []byte{0x44, 0x55}) {
		t.Errorf("subs3 = %x %x %x", subs3[0], subs3[1], subs3[2])
	}
}

func TestSplitContainerRejects(t *testing.T) {
	cases := map[string][]byte{
		"frame nu (não 0x92)":   {0x04, 0xAA, 0xBB},
		"frame nu SID":          {0x80, 0xAA, 0xBB, 0xCC},
		"curto demais":          {0x92, 0x02},
		"count inválido (1)":    {0x92, 0x01, 0xAA, 0xBB},
		"count inválido (9)":    {0x92, 0x09, 0x01, 0xAA},
		"len estoura o payload": {0x92, 0x02, 0xFF, 0xAA, 0xBB},
		"sem bytes pro último":  {0x92, 0x02, 0x02, 0xAA, 0xBB},
	}
	for name, p := range cases {
		if _, ok := splitContainer(p); ok {
			t.Errorf("%s: deveria rejeitar, aceitou", name)
		}
	}
}

func TestDepackSplitRedMain(t *testing.T) {
	// 1 bloco redundante (code=1, size=2) + main de 3 bytes.
	//  header: [0x81(code|0x80), 0x02(size)] depois [0x05] marcador do main (bit alto limpo)
	//  dados: [R1,R2] redundante | [M1,M2,M3] main
	p := []byte{0x81, 0x02, 0x05, 0xA1, 0xA2, 0x4D, 0x4E, 0x4F}
	main, ok := depackSplitRedMain(p)
	if !ok {
		t.Fatalf("RED válido rejeitado")
	}
	if !bytes.Equal(main, []byte{0x4D, 0x4E, 0x4F}) {
		t.Errorf("main = %x, quer 4D4E4F", main)
	}
}

func TestDepackSplitRedRejectsBare(t *testing.T) {
	// Frames nus NÃO podem ser tratados como RED (corromperiam o áudio).
	cases := map[string][]byte{
		"frame nu ativo (TOC<0x80)": {0x04, 0xAA, 0xBB, 0xCC},
		"frame nu SID sem header":   {0x80, 0x01, 0xAA},
		"vazio":                     {},
		"só marcador main":          {0x05},
	}
	for name, p := range cases {
		if _, ok := depackSplitRedMain(p); ok {
			t.Errorf("%s: deveria rejeitar (frame nu), aceitou", name)
		}
	}
}

func TestParseMlowTOC(t *testing.T) {
	if got := parseMlowTOC(0xC0); !got.StdOpus {
		t.Errorf("0xC0 deveria ser std opus")
	}
	if got := parseMlowTOC(0x80); !got.SID {
		t.Errorf("0x80 (bit7) deveria marcar SID")
	}
	// bit6 (VAD) setado => ativo.
	if got := parseMlowTOC(0x40); !got.Active {
		t.Errorf("0x40 (VAD) deveria ser ativo")
	}
}

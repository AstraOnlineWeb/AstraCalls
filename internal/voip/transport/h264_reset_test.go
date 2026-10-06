package transport

import "testing"

// Após um FU-A iniciado mas interrompido por perda, Reset() descarta o fragmento
// pendente — o próximo FU-A completo NÃO é emendado no antigo (frame limpo).
func TestH264DepacketizerResetDropsPartialFUA(t *testing.T) {
	d := &H264Depacketizer{}
	// FU-A start (sem end): tipo 5 (IDR), startBit set. header[0]=0x7C (FU-A, nri alto), header[1]=0x85 (start + type5)
	if out := d.Depacketize([]byte{0x7C, 0x85, 0xAA, 0xBB}); out != nil {
		t.Fatalf("FU-A start não deve emitir NALU ainda, veio %d", len(out))
	}
	// perda no meio -> Reset
	d.Reset()
	if d.fuActive {
		t.Fatal("Reset deveria limpar fuActive")
	}
	// Um fragmento de continuação órfão (sem start) após Reset é ignorado (não emenda).
	if out := d.Depacketize([]byte{0x7C, 0x05, 0xCC}); out != nil {
		t.Fatalf("continuação órfã após Reset deve ser ignorada, veio %d", len(out))
	}
	// Novo NALU single completo decodifica normalmente.
	out := d.Depacketize([]byte{0x65, 0x11, 0x22}) // tipo 5 single
	if len(out) != 1 || out[0][0]&0x1f != 5 {
		t.Fatalf("NALU single após Reset deveria sair limpo, veio %v", out)
	}
}

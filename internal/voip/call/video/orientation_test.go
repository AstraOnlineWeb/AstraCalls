package video

import (
	"testing"

	"wacalls/internal/voip/media"
)

// A orientação vem nos 2 bits baixos do MediaFrameInfo (id 3) da extensão 0xDEBE —
// o mesmo bloco que montamos no envio (buildWhatsappVideoExt).
func TestParseVideoOrientationFromWhatsappExt(t *testing.T) {
	for want := 0; want < 4; want++ {
		fn := uint16(7)
		ext := buildWhatsappVideoExt(videoMediaFrameIDR|uint8(want), &fn, 3)
		h := &media.RtpHeader{Extension: true, ExtensionProfile: videoExtProfile, ExtensionData: ext}
		got, ok := parseVideoOrientation(h)
		if !ok || got != want {
			t.Fatalf("orientation %d: got %d ok=%v", want, got, ok)
		}
	}
	if _, ok := parseVideoOrientation(&media.RtpHeader{Extension: true, ExtensionProfile: 0xBEDE, ExtensionData: []byte{0x30, 1, 0, 0}}); ok {
		t.Fatal("perfil 0xBEDE não deve ser lido como extensão de vídeo do WhatsApp")
	}
	if _, ok := parseVideoOrientation(&media.RtpHeader{}); ok {
		t.Fatal("sem extensão não deve devolver orientação")
	}
}

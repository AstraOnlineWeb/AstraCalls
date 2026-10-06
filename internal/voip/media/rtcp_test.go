package media

import (
	"bytes"
	"encoding/hex"
	"testing"
)

// Vetores do meowcaller (rtp/testdata/kats.json).
func TestBuildSenderReportMatchesKAT(t *testing.T) {
	stats := RtcpSenderStats{PacketsSent: 5, OctetsSent: 600, RtpTimestamp: 1600}
	got := BuildSenderReport(305419896, &stats, 1718000000000)
	want, _ := hex.DecodeString("80c8000612345678ea11180000000000000006400000000500000258")
	if !bytes.Equal(got[:], want) {
		t.Fatalf("sender report = %x, want %x", got[:], want)
	}
}

func TestSrtcpRoundTripAndAuth(t *testing.T) {
	epoch, _ := hex.DecodeString("000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f")
	keys, err := DeriveGroupSrtcpKeys(epoch, "111111111111111:0@lid")
	if err != nil {
		t.Fatal(err)
	}
	// Labels distintos dos da SRTP: a chave de cifra SRTCP não pode coincidir com a
	// chave de cifra SRTP do mesmo participante.
	srtp, _ := DeriveGroupSrtpKeying(epoch, "111111111111111:0@lid")
	ctx, _ := NewSrtpContext(srtp, 4)
	if bytes.Equal(ctx.sessionKey, keys.CipherKey[:]) {
		t.Fatal("SRTCP keys must use distinct KDF labels")
	}
	ssrc := uint32(305419896)
	stats := RtcpSenderStats{PacketsSent: 5, OctetsSent: 600, RtpTimestamp: 1600}
	var cname [WhatsappRtcpCnameLen]byte
	copy(cname[:], "abcde@pj123456.org")
	plain := BuildSenderReportWithSdes(ssrc, &stats, 1718000000000, &cname, true)
	protected, err := ProtectSrtcp(&keys, ssrc, 1, plain)
	if err != nil {
		t.Fatal(err)
	}
	if len(protected) != len(plain)+SrtcpTrailerLen {
		t.Fatalf("protected len %d want %d", len(protected), len(plain)+SrtcpTrailerLen)
	}
	recovered, index, ok := UnprotectSrtcp(&keys, ssrc, protected)
	if !ok || index != 1 || !bytes.Equal(recovered, plain) {
		t.Fatalf("unprotect: ok=%v index=%d equal=%v", ok, index, bytes.Equal(recovered, plain))
	}
	protected[len(protected)-1] ^= 0x01
	if _, _, ok := UnprotectSrtcp(&keys, ssrc, protected); ok {
		t.Fatal("forged SRTCP tag accepted")
	}
}

func TestPliRoundTripDetected(t *testing.T) {
	epoch := make([]byte, 32)
	for i := range epoch {
		epoch[i] = byte(i)
	}
	sender, err := NewSrtcpSender(epoch, "111111111111111:0@lid", 0x11223344, true)
	if err != nil {
		t.Fatal(err)
	}
	pkt, err := sender.PictureLossIndication(0xAABBCCDD)
	if err != nil {
		t.Fatal(err)
	}
	if !IsRtcpPacket(pkt) {
		t.Fatal("PLI not classified as RTCP")
	}
	if s, ok := ParseRtcpSenderSsrc(pkt); !ok || s != 0x11223344 {
		t.Fatalf("sender ssrc = %x ok=%v", s, ok)
	}
	keys := sender.Keying()
	plain, _, ok := UnprotectSrtcp(&keys, 0x11223344, pkt)
	if !ok {
		t.Fatal("unprotect PLI failed")
	}
	if !RtcpRequestsKeyframe(plain, 0xAABBCCDD) {
		t.Fatal("PLI for our video SSRC not detected")
	}
	if RtcpRequestsKeyframe(plain, 0x01020304) {
		t.Fatal("PLI matched wrong SSRC")
	}
	sr, _ := sender.SenderReport(RtcpSenderStats{}, 1718000000000)
	plainSR, _, ok := UnprotectSrtcp(&keys, 0x11223344, sr)
	if !ok || RtcpRequestsKeyframe(plainSR, 0xAABBCCDD) {
		t.Fatal("SR+SDES must unprotect and not look like a keyframe request")
	}
}

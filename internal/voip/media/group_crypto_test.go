package media

import (
	"encoding/hex"
	"testing"

	"wacalls/internal/voip/core"
)

// Vetores DOURADOS gerados com a própria lib meowcaller (fonte da verdade do
// protocolo) — garantem que a nossa derivação casa byte-a-byte com o servidor.
//   callID = "CALL123"; jid = "123456789@lid"; epoch = bytes 1..32.

func TestFormatParticipantID(t *testing.T) {
	cases := map[string]string{
		"123456789@lid":                "123456789:0@lid",              // @lid sem device -> :0
		"987654321:7@lid/phone":        "987654321:7@lid",              // tira recurso, mantém device
		"5511999999999@s.whatsapp.net": "5511999999999@s.whatsapp.net", // passthrough
		"semdominio":                   "semdominio",
	}
	for in, want := range cases {
		if got := FormatParticipantID(in); got != want {
			t.Errorf("FormatParticipantID(%q) = %q, quer %q", in, got, want)
		}
	}
}

func TestDeriveParticipantSSRCGolden(t *testing.T) {
	const callID = "CALL123"
	pid := FormatParticipantID("123456789@lid")
	want := map[uint32]uint32{0: 1876065878, 2: 431449605, 6: 1077896786}
	for slot, exp := range want {
		got, err := DeriveParticipantSSRC(callID, pid, slot)
		if err != nil {
			t.Fatalf("slot %d: %v", slot, err)
		}
		if got != exp {
			t.Errorf("SSRC slot %d = %d, quer %d", slot, got, exp)
		}
	}
	nine, err := DeriveRelayStreamSSRCs(callID, pid)
	if err != nil {
		t.Fatal(err)
	}
	expNine := [9]uint32{1876065878, 3805756336, 2097628768, 431449605, 1565403005, 2382823777, 3179225172, 7954009, 1077896786}
	if nine != expNine {
		t.Errorf("nine = %v, quer %v", nine, expNine)
	}
}

func TestDeriveGroupSrtpKeyingGolden(t *testing.T) {
	pid := FormatParticipantID("123456789@lid")
	epoch := make([]byte, 32)
	for i := range epoch {
		epoch[i] = byte(i + 1)
	}
	km, err := DeriveGroupSrtpKeying(epoch, pid)
	if err != nil {
		t.Fatal(err)
	}
	// Deriva as session keys como a nossa SRTP faz (mesmo AES-CM KDF) e compara com
	// o cipherKey/salt que o meowcaller produz.
	cipher, err := deriveSrtpKey(km.MasterKey, km.MasterSalt, core.SRTPLabelEncryption, 16)
	if err != nil {
		t.Fatal(err)
	}
	salt, err := deriveSrtpKey(km.MasterKey, km.MasterSalt, core.SRTPLabelSalt, 14)
	if err != nil {
		t.Fatal(err)
	}
	if hex.EncodeToString(cipher) != "4fefc3dd41c0da3f8a8bdf22e33a3cb6" {
		t.Errorf("cipherKey = %s", hex.EncodeToString(cipher))
	}
	if hex.EncodeToString(salt) != "d6beb0714cc670c79e42ef03c281" {
		t.Errorf("salt = %s", hex.EncodeToString(salt))
	}
}

func TestDeriveGroupSrtpKeyingShortEpoch(t *testing.T) {
	if _, err := DeriveGroupSrtpKeying(make([]byte, 16), "x"); err == nil {
		t.Error("epoch curto deveria falhar")
	}
}

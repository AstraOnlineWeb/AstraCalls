package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/json"
	"strings"
	"testing"

	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/proto/waWeb"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/util/cbcutil"
	"go.mau.fi/whatsmeow/util/hkdfutil"
	"google.golang.org/protobuf/proto"
)

func lidGroup() *types.GroupInfo {
	return &types.GroupInfo{
		AddressingMode: types.AddressingModeLID,
		Participants: []types.GroupParticipant{
			{JID: types.NewJID("111", types.HiddenUserServer), PhoneNumber: types.NewJID("5561984598027", types.DefaultUserServer), LID: types.NewJID("111", types.HiddenUserServer)},
			{JID: types.NewJID("222", types.HiddenUserServer), PhoneNumber: types.NewJID("557791210388", types.DefaultUserServer), LID: types.NewJID("222", types.HiddenUserServer)},
			{JID: types.NewJID("999", types.HiddenUserServer), PhoneNumber: types.NewJID("5511900000000", types.DefaultUserServer), LID: types.NewJID("999", types.HiddenUserServer)},
		},
	}
}

func TestGroupHistoryAudience(t *testing.T) {
	own := types.NewJID("999", types.HiddenUserServer)
	// telefone com 9º dígito a mais (13 díg.) casa com o PN de 12 díg. do grupo; LID casa direto
	rec, others, err := groupHistoryAudience(lidGroup(), []string{"+55 77 99121-0388"}, own)
	if err != nil {
		t.Fatal(err)
	}
	if len(rec) != 1 || rec[0].String() != "222@lid" {
		t.Fatalf("receivers = %v", rec)
	}
	if len(others) != 1 || others[0].String() != "111@lid" {
		t.Fatalf("others = %v (não deve incluir a própria conta)", others)
	}
	if rec, _, err := groupHistoryAudience(lidGroup(), []string{"111@lid"}, own); err != nil || rec[0].User != "111" {
		t.Fatalf("LID direto: %v %v", rec, err)
	}
	if _, _, err := groupHistoryAudience(lidGroup(), []string{"5521988887777"}, own); err == nil || !strings.Contains(err.Error(), "não são membros") {
		t.Fatalf("não-membro deveria falhar: %v", err)
	}
	if _, _, err := groupHistoryAudience(lidGroup(), []string{"5511900000000"}, own); err == nil {
		t.Fatal("compartilhar com a própria conta deveria falhar")
	}
}

func TestParseShareHistory(t *testing.T) {
	cases := map[string]int{``: 0, `null`: 0, `false`: 0, `0`: 0, `true`: 50, `25`: 25, `"75"`: 75, `-3`: 0}
	for in, want := range cases {
		if got := parseShareHistory(json.RawMessage(in)); got != want {
			t.Errorf("parseShareHistory(%q) = %d; quer %d", in, got, want)
		}
	}
}

func TestGroupHistoryBundleRoundTripAndCrypto(t *testing.T) {
	msgs := []*waWeb.WebMessageInfo{{
		Key: &waCommon.MessageKey{RemoteJID: proto.String("120363@g.us"), ID: proto.String("ABC"),
			FromMe: proto.Bool(false), Participant: proto.String("111@lid")},
		Message:          &waE2E.Message{Conversation: proto.String("oi grupo")},
		MessageTimestamp: proto.Uint64(1700000000),
	}}
	blob, err := encodeGroupHistoryBundle(msgs)
	if err != nil {
		t.Fatal(err)
	}
	if len(blob) < 2 || blob[0] != 0x78 {
		t.Fatalf("blob não é zlib (cabeçalho %x)", blob[:2])
	}
	gh, err := decodeGroupHistoryBundle(blob)
	if err != nil || len(gh.GetMessages()) != 1 || gh.GetMessages()[0].GetMessage().GetConversation() != "oi grupo" {
		t.Fatalf("ida e volta falhou: %v %v", gh, err)
	}

	em, err := encryptGroupHistoryMedia(blob)
	if err != nil {
		t.Fatal(err)
	}
	// decifra como o receptor faria (HKDF "Group History", HMAC truncado, AES-CBC)
	exp := hkdfutil.SHA256(em.MediaKey, nil, []byte("Group History"), 112)
	iv, ck, mk := exp[:16], exp[16:48], exp[48:80]
	ct := append([]byte(nil), em.Upload[:len(em.Upload)-10]...)
	mac := em.Upload[len(em.Upload)-10:]
	h := hmac.New(sha256.New, mk)
	h.Write(iv)
	h.Write(ct)
	if !hmac.Equal(h.Sum(nil)[:10], mac) {
		t.Fatal("HMAC não confere")
	}
	pt, err := cbcutil.Decrypt(ck, iv, ct)
	if err != nil || string(pt) != string(blob) {
		t.Fatalf("decifrar falhou: %v", err)
	}
	if sum := sha256.Sum256(blob); string(sum[:]) != string(em.FileSHA256) || em.FileLength != uint64(len(blob)) {
		t.Fatal("FileSHA256/FileLength errados")
	}
	if sum := sha256.Sum256(em.Upload); string(sum[:]) != string(em.FileEncSHA256) {
		t.Fatal("FileEncSHA256 errado")
	}
}

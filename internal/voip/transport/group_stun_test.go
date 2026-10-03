package transport

import (
	"encoding/hex"
	"testing"
)

// Vetores DOURADOS gerados com a lib meowcaller (stun.BuildWasmStunAllocateRequest
// WithGroupSubscriptionsAndHBHFEC e stun.EncodeXorRelayEndpoint) — garantem que o
// nosso Allocate de grupo bate byte-a-byte com o do WhatsApp Web.
//   tx=[1..12] relayToken=AABB ep=1.2.3.4:3478 streams=[1..9] appData=10
//   hbh=[11,12] ik="0123456789abcdef0123456789abcdef"

func TestEncodeXorRelayEndpointBytesGolden(t *testing.T) {
	ep, ok := EncodeXorRelayEndpointBytes("1.2.3.4", 3478)
	if !ok {
		t.Fatal("ok=false")
	}
	if got := hex.EncodeToString(ep[:]); got != "2c842010a746" {
		t.Errorf("xor = %s, quer 2c842010a746", got)
	}
	if _, ok := EncodeXorRelayEndpointBytes("1.2.3", 3478); ok {
		t.Error("ipv4 incompleto deveria falhar")
	}
}

func goldenInputs() ([12]byte, []byte, [6]byte, [9]uint32, [2]uint32, []byte) {
	tx := [12]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12}
	relayToken := []byte{0xAA, 0xBB}
	ep, _ := EncodeXorRelayEndpointBytes("1.2.3.4", 3478)
	streams := [9]uint32{1, 2, 3, 4, 5, 6, 7, 8, 9}
	hbh := [2]uint32{11, 12}
	ik := []byte("0123456789abcdef0123456789abcdef")
	return tx, relayToken, ep, streams, hbh, ik
}

func TestBuildGroupAllocateGolden(t *testing.T) {
	tx, relayToken, ep, streams, hbh, ik := goldenInputs()
	const want = "000300d42112a4420102030405060708090a0b0c40000002aabb00004025003e0a130a110a030405061204080010011204080110010a070a050a030708090a0f0a0d0a0301020312020800120208010a0d0a0b0a010a120208001202080100004021000812020800120208014024004c0a0218010a04100118020a04100218030a04080118040a060801100118050a060801100218060a04080218070a060802100118080a060802100218090a0608031003180b0a0608041003180c805a0001020000000016000800012c842010a7460008001435709b3ad6f3f39e3ec15d4a7756ae31eb1fd5d6"
	got := hex.EncodeToString(BuildGroupAllocate(tx, relayToken, ep, streams, 10, hbh, []uint32{0, 1}, ik))
	if got != want {
		t.Errorf("group allocate NAO bate:\n got=%s\nwant=%s", got, want)
	}
}

func TestBuildGroupAllocateNoPidsGolden(t *testing.T) {
	tx, relayToken, ep, streams, hbh, ik := goldenInputs()
	const want = "0003006c2112a4420102030405060708090a0b0c40000002aabb00004024003c0a0218010a04100118020a04100218030a04080118040a060801100118050a060801100218060a04080218070a060802100118080a060802100218090016000800012c842010a74600080014d6c046449885c01fe20a3652929b9e7b64a594cd"
	got := hex.EncodeToString(BuildGroupAllocate(tx, relayToken, ep, streams, 10, hbh, nil, ik))
	if got != want {
		t.Errorf("group allocate (sem pids) NAO bate:\n got=%s\nwant=%s", got, want)
	}
}

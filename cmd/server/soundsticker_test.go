package main

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"testing"
)

func TestWithSoundExif(t *testing.T) {
	audio := []byte("OggS fake")
	out, err := withSoundExif(soundStickerDefaultWebP, audio, soundStickerPack{})
	if err != nil {
		t.Fatal(err)
	}
	if int(binary.LittleEndian.Uint32(out[4:8])) != len(out)-8 {
		t.Fatal("tamanho RIFF errado")
	}
	chunks, err := readWebPChunks(out)
	if err != nil {
		t.Fatal(err)
	}
	var exif []byte
	n := 0
	for _, c := range chunks {
		if c.id == "VP8X" && c.payload[0]&0x08 == 0 {
			t.Fatal("flag EXIF ausente")
		}
		if c.id == "EXIF" {
			exif = c.payload
			n++
		}
	}
	if n != 1 {
		t.Fatalf("EXIF chunks=%d", n)
	}
	var js map[string]any
	if err := json.Unmarshal(exif[22:], &js); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(audio)
	if js["sound-sha256"] != base64.StdEncoding.EncodeToString(sum[:]) {
		t.Fatal("sound-sha256 errado")
	}
	// reaplicar substitui o EXIF
	out2, _ := withSoundExif(out, []byte("x"), soundStickerPack{})
	c2, _ := readWebPChunks(out2)
	cnt := 0
	for _, c := range c2 {
		if c.id == "EXIF" {
			cnt++
		}
	}
	if cnt != 1 {
		t.Fatal("EXIF duplicado")
	}
	if !webpIsAnimated(out) {
		t.Fatal("deveria ser animado")
	}
	if _, sec, err := toOpusOgg(soundStickerDefaultOgg); err != nil || sec == 0 {
		t.Fatalf("ogg: %v %d", err, sec)
	}
}

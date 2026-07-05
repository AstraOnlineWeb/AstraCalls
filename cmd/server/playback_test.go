package main

import "testing"

func TestDecodeWAVPCM16MonoAndResample(t *testing.T) {
	raw := makeTestWAV([]int16{0, 32767, -32768, 16384}, 8000, 1)
	wav, err := decodeWAVPCM16(raw)
	if err != nil {
		t.Fatalf("decodeWAVPCM16() error = %v", err)
	}
	if wav.SampleRate != 8000 {
		t.Fatalf("sample rate = %d, want 8000", wav.SampleRate)
	}
	if wav.Channels != 1 {
		t.Fatalf("channels = %d, want 1", wav.Channels)
	}
	pcm16 := resampleTo16k(wav.Samples, wav.SampleRate)
	if got, want := len(pcm16), 8; got != want {
		t.Fatalf("resampled len = %d, want %d", got, want)
	}
	if durationMsForPCM16(pcm16) != 1 {
		t.Fatalf("duration ms = %d, want 1", durationMsForPCM16(pcm16))
	}
}

func TestDecodeWAVPCM16RejectsNonWav(t *testing.T) {
	if _, err := decodeWAVPCM16([]byte("not a wav")); err == nil {
		t.Fatal("expected non-wav error")
	}
}

func TestPlaybackRegistrySingleFlight(t *testing.T) {
	ac := &activeCall{}
	ch, ok := ac.startPlayback()
	if !ok || ch == nil {
		t.Fatal("first playback should start")
	}
	if _, ok := ac.startPlayback(); ok {
		t.Fatal("second playback should be rejected")
	}
	ac.finishPlayback(ch)
	if _, ok := ac.startPlayback(); !ok {
		t.Fatal("playback should start after finish")
	}
}

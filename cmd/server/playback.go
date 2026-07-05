package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
)

const maxPlaybackAudioBytes = 12 * 1024 * 1024

type wavPCM struct {
	Samples    []float32
	SampleRate int
	Channels   int
}

func decodeWAVPCM16(raw []byte) (wavPCM, error) {
	if len(raw) < 44 || string(raw[0:4]) != "RIFF" || string(raw[8:12]) != "WAVE" {
		return wavPCM{}, errors.New("audio must be a RIFF/WAVE file")
	}

	var channels uint16
	var sampleRate uint32
	var bitsPerSample uint16
	var audioFormat uint16
	var data []byte

	for off := 12; off+8 <= len(raw); {
		chunkID := string(raw[off : off+4])
		chunkSize := int(binary.LittleEndian.Uint32(raw[off+4 : off+8]))
		off += 8
		if chunkSize < 0 || off+chunkSize > len(raw) {
			return wavPCM{}, errors.New("invalid wav chunk size")
		}
		chunk := raw[off : off+chunkSize]
		switch chunkID {
		case "fmt ":
			if len(chunk) < 16 {
				return wavPCM{}, errors.New("invalid wav fmt chunk")
			}
			audioFormat = binary.LittleEndian.Uint16(chunk[0:2])
			channels = binary.LittleEndian.Uint16(chunk[2:4])
			sampleRate = binary.LittleEndian.Uint32(chunk[4:8])
			bitsPerSample = binary.LittleEndian.Uint16(chunk[14:16])
		case "data":
			data = chunk
		}
		off += chunkSize
		if chunkSize%2 == 1 {
			off++
		}
	}

	if audioFormat != 1 {
		return wavPCM{}, errors.New("only PCM wav is supported")
	}
	if channels != 1 && channels != 2 {
		return wavPCM{}, errors.New("wav must be mono or stereo")
	}
	if sampleRate == 0 {
		return wavPCM{}, errors.New("wav sample rate is missing")
	}
	if bitsPerSample != 16 {
		return wavPCM{}, errors.New("only 16-bit PCM wav is supported")
	}
	if len(data) == 0 {
		return wavPCM{}, errors.New("wav data chunk is empty")
	}
	bytesPerFrame := int(channels) * 2
	if len(data)%bytesPerFrame != 0 {
		return wavPCM{}, errors.New("wav data is not frame-aligned")
	}

	frames := len(data) / bytesPerFrame
	samples := make([]float32, frames)
	for i := 0; i < frames; i++ {
		if channels == 1 {
			v := int16(binary.LittleEndian.Uint16(data[i*2 : i*2+2]))
			samples[i] = float32(v) / 32768
			continue
		}
		base := i * 4
		l := int16(binary.LittleEndian.Uint16(data[base : base+2]))
		r := int16(binary.LittleEndian.Uint16(data[base+2 : base+4]))
		samples[i] = (float32(l) + float32(r)) / (2 * 32768)
	}

	return wavPCM{Samples: samples, SampleRate: int(sampleRate), Channels: int(channels)}, nil
}

func resampleTo16k(in []float32, sampleRate int) []float32 {
	if sampleRate == recorderSampleRate {
		out := make([]float32, len(in))
		copy(out, in)
		return out
	}
	if len(in) == 0 || sampleRate <= 0 {
		return nil
	}
	outLen := int(math.Round(float64(len(in)) * float64(recorderSampleRate) / float64(sampleRate)))
	if outLen <= 0 {
		return nil
	}
	out := make([]float32, outLen)
	for i := range out {
		pos := float64(i) * float64(sampleRate) / float64(recorderSampleRate)
		idx := int(pos)
		frac := float32(pos - float64(idx))
		if idx >= len(in)-1 {
			out[i] = in[len(in)-1]
			continue
		}
		out[i] = in[idx] + (in[idx+1]-in[idx])*frac
	}
	return out
}

func playbackID(sessionID, callID string) string {
	return safePathPart(sessionID) + ":" + safePathPart(callID)
}

func (s *server) readPlaybackAudio(r *http.Request) ([]float32, int, error) {
	if err := r.ParseMultipartForm(maxPlaybackAudioBytes); err != nil {
		return nil, 0, err
	}
	file, header, err := r.FormFile("audio")
	if err != nil {
		return nil, 0, errors.New("multipart field 'audio' is required")
	}
	defer file.Close()
	name := "audio.wav"
	if header != nil && header.Filename != "" {
		name = header.Filename
	}
	if !strings.HasSuffix(strings.ToLower(name), ".wav") {
		return nil, 0, errors.New("only .wav upload is supported in this spike")
	}
	raw, err := io.ReadAll(io.LimitReader(file, maxPlaybackAudioBytes+1))
	if err != nil {
		return nil, 0, err
	}
	if len(raw) > maxPlaybackAudioBytes {
		return nil, 0, fmt.Errorf("audio exceeds %d bytes", maxPlaybackAudioBytes)
	}
	wav, err := decodeWAVPCM16(raw)
	if err != nil {
		return nil, 0, err
	}
	pcm16 := resampleTo16k(wav.Samples, wav.SampleRate)
	return pcm16, wav.SampleRate, nil
}

func durationMsForPCM16(pcm []float32) int64 {
	return int64(math.Round(float64(len(pcm)) * 1000 / recorderSampleRate))
}

func (s *server) doPlayAudio(sess *Session, w http.ResponseWriter, r *http.Request) {
	callID := r.PathValue("id")
	ac, ok := sess.reg.get(callID)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such call"})
		return
	}
	if !ac.cm.IsPlaybackReady() {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "call is not connected or media is not ready"})
		return
	}
	pcm16, sourceRate, err := s.readPlaybackAudio(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if len(pcm16) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "audio has no samples"})
		return
	}

	cancel, ok := ac.startPlayback()
	if !ok {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "playback already running"})
		return
	}

	durationMs := durationMsForPCM16(pcm16)
	go func() {
		defer ac.finishPlayback(cancel)
		ctx, stop := context.WithCancel(context.Background())
		defer stop()
		go func() {
			<-cancel
			stop()
		}()
		err := ac.cm.PlayCapturedPCM(ctx, pcm16, func(frame []float32) {
			if ac.recorder != nil {
				ac.recorder.AddOperatorPCM(frame)
			}
		})
		if err != nil && !errors.Is(err, context.Canceled) {
			s.log.Warn("playback failed", "call_id", callID, "err", err)
		}
	}()

	writeJSON(w, http.StatusAccepted, map[string]any{
		"status":        "playing",
		"callId":        callID,
		"durationMs":    durationMs,
		"sourceRate":    sourceRate,
		"targetRate":    recorderSampleRate,
		"samples":       len(pcm16),
		"contentFormat": "wav/pcm16",
	})
}

func makeTestWAV(samples []int16, sampleRate int, channels int) []byte {
	if channels <= 0 {
		channels = 1
	}
	buf := bytes.NewBuffer(nil)
	dataSize := uint32(len(samples) * 2)
	byteRate := uint32(sampleRate * channels * 2)
	blockAlign := uint16(channels * 2)
	buf.WriteString("RIFF")
	_ = binary.Write(buf, binary.LittleEndian, uint32(36)+dataSize)
	buf.WriteString("WAVEfmt ")
	_ = binary.Write(buf, binary.LittleEndian, uint32(16))
	_ = binary.Write(buf, binary.LittleEndian, uint16(1))
	_ = binary.Write(buf, binary.LittleEndian, uint16(channels))
	_ = binary.Write(buf, binary.LittleEndian, uint32(sampleRate))
	_ = binary.Write(buf, binary.LittleEndian, byteRate)
	_ = binary.Write(buf, binary.LittleEndian, blockAlign)
	_ = binary.Write(buf, binary.LittleEndian, uint16(16))
	buf.WriteString("data")
	_ = binary.Write(buf, binary.LittleEndian, dataSize)
	for _, sample := range samples {
		_ = binary.Write(buf, binary.LittleEndian, sample)
	}
	return buf.Bytes()
}

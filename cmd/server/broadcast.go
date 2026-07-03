package main

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"math/rand"
	"net/http"
	"os/exec"
	"sync"
	"time"

	"wacalls/internal/voip/core"
)

// Disparo em massa de ligações com áudio pré-gravado.
//
// Fluxo por número: startOutgoing → espera atender (broker: status "connected")
// → "pump" de áudio (ffmpeg decodifica 1x para PCM 16 kHz mono; a cada 20 ms
// alimenta cm.FeedCapturedPCM, que encoda no codec MLow do WhatsApp e envia) →
// EndCall ao fim do áudio (ou hangup_after_ms). Sem atender em max_ring_ms →
// desliga e marca no-answer. Concorrência limitada ao maxCalls da sessão + gap
// entre disparos.
//
// GUARDA-CORPOS: use só com contatos que consentiram (opt-in). Ligação em massa
// não solicitada viola o WhatsApp (risco de ban do número) e a legislação
// (Anatel/CDC/LGPD). O filtro de opt-in/opt-out é responsabilidade de quem
// dispara (ex.: o Chatwoot só envia a lista já consentida).

const (
	bcSampleRate    = 16000
	bcFrameSamples  = bcSampleRate / 50 // 20 ms = 320 amostras @ 16 kHz
	bcDefaultRingMs = 45000
	bcDefaultGapMs  = 1500
	bcMaxConcurrent = 16
)

type broadcastRequest struct {
	AudioURL      string   `json:"audio_url"`
	AudioBase64   string   `json:"audio_base64"`
	Numbers       []string `json:"numbers"`
	Concurrency   int      `json:"concurrency"`
	GapMs         int      `json:"gap_ms"`
	MaxRingMs     int      `json:"max_ring_ms"`
	HangupAfterMs int      `json:"hangup_after_ms"`
}

type broadcastResult struct {
	Number     string `json:"number"`
	To         string `json:"to"`
	CallID     string `json:"callId,omitempty"`
	Status     string `json:"status"` // queued|ringing|answered|completed|no-answer|failed
	DurationMs int    `json:"durationMs,omitempty"`
	Error      string `json:"error,omitempty"`
}

type broadcastCampaign struct {
	ID        string
	SessionID string
	Total     int
	mu        sync.Mutex
	results   map[string]*broadcastResult // por número
	order     []string
	done      bool
}

func (c *broadcastCampaign) set(number string, fn func(r *broadcastResult)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	r := c.results[number]
	if r == nil {
		r = &broadcastResult{Number: number, Status: "queued"}
		c.results[number] = r
		c.order = append(c.order, number)
	}
	fn(r)
}

func (c *broadcastCampaign) snapshot() []broadcastResult {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]broadcastResult, 0, len(c.order))
	for _, n := range c.order {
		out = append(out, *c.results[n])
	}
	return out
}

// store em memória das campanhas (efêmero; o rastreio real vai pelo webhook).
type broadcastStore struct {
	mu sync.Mutex
	m  map[string]*broadcastCampaign
}

var broadcasts = &broadcastStore{m: map[string]*broadcastCampaign{}}

func (s *broadcastStore) add(c *broadcastCampaign) {
	s.mu.Lock()
	s.m[c.ID] = c
	s.mu.Unlock()
}
func (s *broadcastStore) get(id string) *broadcastCampaign {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.m[id]
}

// decodePCM16 usa ffmpeg para transformar qualquer áudio em PCM float32 mono
// @ 16 kHz (a taxa do pipeline de chamada). Feito uma vez por campanha.
func decodePCM16(data []byte) ([]float32, error) {
	cmd := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error",
		"-i", "pipe:0", "-ac", "1", "-ar", "16000", "-f", "f32le", "pipe:1")
	cmd.Stdin = bytes.NewReader(data)
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("ffmpeg decode: %v: %s", err, errb.String())
	}
	raw := out.Bytes()
	pcm := make([]float32, len(raw)/4)
	for i := range pcm {
		pcm[i] = math.Float32frombits(binary.LittleEndian.Uint32(raw[i*4:]))
	}
	return pcm, nil
}

// POST /api/sessions/{sid}/broadcast
func (s *server) handleBroadcast(w http.ResponseWriter, r *http.Request) {
	sess := s.pairedSession(w, r.PathValue("sid"))
	if sess == nil {
		return
	}
	var b broadcastRequest
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "payload inválido"})
		return
	}
	if len(b.Numbers) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "numbers obrigatório"})
		return
	}
	if b.AudioURL == "" && b.AudioBase64 == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "audio_url ou audio_base64 obrigatório"})
		return
	}
	media, err := fetchMedia(b.AudioBase64, b.AudioURL)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "baixar áudio: " + err.Error()})
		return
	}
	pcm, err := decodePCM16(media)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if len(pcm) < bcFrameSamples {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "áudio vazio/curto demais"})
		return
	}

	camp := &broadcastCampaign{
		ID:        newSessionID(),
		SessionID: sess.id,
		Total:     len(b.Numbers),
		results:   map[string]*broadcastResult{},
	}
	for _, n := range b.Numbers {
		camp.set(n, func(*broadcastResult) {})
	}
	broadcasts.add(camp)

	go sess.runCampaign(camp, pcm, b)

	writeJSON(w, http.StatusOK, map[string]any{
		"campaignId": camp.ID,
		"total":      camp.Total,
		"seconds":    len(pcm) / bcSampleRate,
	})
}

// GET /api/sessions/{sid}/broadcast/{cid}
func (s *server) handleBroadcastStatus(w http.ResponseWriter, r *http.Request) {
	camp := broadcasts.get(r.PathValue("cid"))
	if camp == nil || camp.SessionID != r.PathValue("sid") {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "campanha não encontrada"})
		return
	}
	camp.mu.Lock()
	done := camp.done
	camp.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{
		"campaignId": camp.ID,
		"total":      camp.Total,
		"done":       done,
		"results":    camp.snapshot(),
	})
}

// runCampaign disca todos os números respeitando a concorrência e o gap.
func (s *Session) runCampaign(camp *broadcastCampaign, pcm []float32, req broadcastRequest) {
	conc := req.Concurrency
	if conc < 1 {
		conc = 1
	}
	if max := s.mgr.maxCalls; max > 0 && conc > max {
		conc = max // nunca fura o limite de chamadas simultâneas da sessão
	}
	if conc > bcMaxConcurrent {
		conc = bcMaxConcurrent
	}
	gap := req.GapMs
	if gap <= 0 {
		gap = bcDefaultGapMs
	}

	sem := make(chan struct{}, conc)
	var wg sync.WaitGroup
	for _, number := range req.Numbers {
		sem <- struct{}{}
		wg.Add(1)
		go func(num string) {
			defer wg.Done()
			defer func() { <-sem }()
			s.broadcastOne(camp, num, pcm, req)
		}(number)
		// gap + jitter entre disparos (evita padrão robótico)
		time.Sleep(time.Duration(gap+rand.Intn(gap+1)) * time.Millisecond)
	}
	wg.Wait()
	camp.mu.Lock()
	camp.done = true
	camp.mu.Unlock()
	s.dispatchWebhook("broadcast", map[string]any{"campaignId": camp.ID, "event": "campaign_done", "total": camp.Total})
}

func (s *Session) broadcastOne(camp *broadcastCampaign, number string, pcm []float32, req broadcastRequest) {
	emit := func(status string, extra map[string]any) {
		m := map[string]any{"campaignId": camp.ID, "number": number, "status": status}
		for k, v := range extra {
			m[k] = v
		}
		s.dispatchWebhook("broadcast", m)
	}
	fail := func(msg string) {
		camp.set(number, func(r *broadcastResult) { r.Status = "failed"; r.Error = msg })
		emit("failed", map[string]any{"error": msg})
	}

	peer, err := resolveRecipient(number)
	if err != nil {
		fail("número inválido")
		return
	}
	callID, err := s.startOutgoing(s.mgr.appCtx, peer, false)
	if err != nil {
		fail(err.Error())
		return
	}
	camp.set(number, func(r *broadcastResult) { r.To = peer.String(); r.CallID = callID; r.Status = "ringing" })
	emit("ringing", map[string]any{"to": peer.String(), "callId": callID})

	// espera atender (status "connected") ou timeout de toque
	ringMs := req.MaxRingMs
	if ringMs <= 0 {
		ringMs = bcDefaultRingMs
	}
	deadline := time.Now().Add(time.Duration(ringMs) * time.Millisecond)
	answered := false
	for time.Now().Before(deadline) {
		rec, ok := s.mgr.broker.getCall(callID)
		if !ok || rec == nil {
			time.Sleep(150 * time.Millisecond)
			continue
		}
		if rec.Status == StatusConnected {
			answered = true
			break
		}
		if rec.Status == StatusEnded {
			break // recusada/indisponível antes de atender
		}
		time.Sleep(150 * time.Millisecond)
	}
	if !answered {
		s.terminateCall(callID, core.EndCallReasonUserEnded)
		camp.set(number, func(r *broadcastResult) { r.Status = "no-answer" })
		emit("no-answer", nil)
		return
	}
	camp.set(number, func(r *broadcastResult) { r.Status = "answered" })
	emit("answered", nil)

	dur := s.pumpAudio(callID, pcm, req.HangupAfterMs)
	s.terminateCall(callID, core.EndCallReasonUserEnded)
	camp.set(number, func(r *broadcastResult) { r.Status = "completed"; r.DurationMs = dur })
	emit("completed", map[string]any{"durationMs": dur})
}

// pumpAudio toca o PCM na chamada em ritmo real (frames de 20 ms) via
// FeedCapturedPCM (o MLow encoda p/ o WhatsApp). Para no fim do áudio, se a
// chamada cair, ou ao atingir hangupAfterMs. Devolve a duração tocada (ms).
func (s *Session) pumpAudio(callID string, pcm []float32, hangupAfterMs int) int {
	start := time.Now()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for i := 0; i < len(pcm); i += bcFrameSamples {
		<-ticker.C
		if hangupAfterMs > 0 && time.Since(start) >= time.Duration(hangupAfterMs)*time.Millisecond {
			break
		}
		rec, ok := s.mgr.broker.getCall(callID)
		if !ok || rec == nil || rec.Status == StatusEnded {
			break // a outra ponta desligou
		}
		ac, ok := s.reg.get(callID)
		if !ok {
			break
		}
		end := min(i+bcFrameSamples, len(pcm))
		ac.cm.FeedCapturedPCM(pcm[i:end])
	}
	return int(time.Since(start).Milliseconds())
}

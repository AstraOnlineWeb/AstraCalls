package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"wacalls/internal/voip/call/groupmix"
	"wacalls/internal/voip/media"
)

// =============================================================================
// Espiar a chamada (escuta em tempo real, só ouvir)
// =============================================================================
//
// Um "espião" (supervisor, auditoria, agente do AstraChat…) abre uma conexão à
// chamada 1:1 como se fosse a ligação, mas SÓ recebe áudio: o mix dos dois lados
// (interlocutor do WhatsApp + atendente), mono 16 kHz. Nada do que ele manda é
// injetado na chamada e a presença dele não mexe na sinalização com o WhatsApp —
// a ligação segue normal para as duas pontas; o atendente só é avisado por SSE
// (call-action kind=spy) se a UI quiser mostrar.
//
// Transportes:
//   - WebRTC: POST /calls/{id}/spy {sdp_offer} → {sdp_answer, spy_id}. O cliente
//     oferece um transceiver de áudio recvonly; o servidor responde com Opus.
//   - WebSocket: GET /calls/{id}/spy/ws → frames binários PCM16 LE 16 kHz (mesmo
//     contrato "pcm16" da ponte da chamada, só downlink; o uplink é ignorado).
//
// Vários espiões por chamada são permitidos. Espiar não reivindica a chamada
// (owner) nem fecha a ponte do atendente. Só áudio (vídeo não é espelhado).

const (
	spySidePeer  = "peer"  // interlocutor (WhatsApp → nós)
	spySideAgent = "agent" // atendente/consumidor (nós → WhatsApp)
)

// spySink é um destino de áudio de espião (WebRTC ou WebSocket).
type spySink interface {
	WritePCM(pcm16 []float32) error
	Close()
}

// spyHub junta os espiões de UMA chamada. O áudio dos dois lados entra por feed()
// e é somado pelo mixer de grupo (groupmix) em chunks de 10ms, reenquadrado em
// frames de 960 amostras (60ms, igual ao MLow) e replicado a todos os sinks.
// O laço de mixagem só roda enquanto houver pelo menos um espião.
type spyHub struct {
	n     atomic.Int32 // nº de sinks — lido no hot path de áudio sem lock
	mu    sync.Mutex
	sinks map[string]spySink
	mixer *groupmix.Mixer
	stop  chan struct{}
	// OnChange é chamado (fora do lock) quando o número de espiões muda.
	OnChange func(n int)
}

func newSpyHub() *spyHub {
	return &spyHub{sinks: map[string]spySink{}, mixer: groupmix.NewMixer()}
}

func newSpyID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// feed enfileira áudio de um dos lados. Sem espiões é um no-op barato (um load
// atômico), então pode ficar no caminho quente do áudio da chamada.
func (h *spyHub) feed(side string, pcm []float32) {
	if h == nil || len(pcm) == 0 || h.n.Load() == 0 {
		return
	}
	h.mixer.Add(side, pcm)
}

// add registra um espião e devolve o id dele. O primeiro espião liga o mixLoop.
func (h *spyHub) add(sink spySink) string {
	id := newSpyID()
	h.mu.Lock()
	h.sinks[id] = sink
	n := len(h.sinks)
	h.n.Store(int32(n))
	if h.stop == nil {
		h.stop = make(chan struct{})
		go h.mixLoop(h.stop)
	}
	h.mu.Unlock()
	if h.OnChange != nil {
		h.OnChange(n)
	}
	return id
}

// remove tira o espião pelo id e fecha o transporte dele.
func (h *spyHub) remove(id string) bool {
	h.mu.Lock()
	sink, ok := h.sinks[id]
	if !ok {
		h.mu.Unlock()
		return false
	}
	delete(h.sinks, id)
	n := h.afterRemoveLocked()
	h.mu.Unlock()
	sink.Close()
	if h.OnChange != nil {
		h.OnChange(n)
	}
	return true
}

// removeSink tira o espião pelo ponteiro (usado pelos callbacks de fechamento do
// transporte, que não precisam conhecer o id). Idempotente.
func (h *spyHub) removeSink(sink spySink) {
	if h == nil {
		return
	}
	h.mu.Lock()
	found := ""
	for id, s := range h.sinks {
		if s == sink {
			found = id
			break
		}
	}
	if found == "" {
		h.mu.Unlock()
		return
	}
	delete(h.sinks, found)
	n := h.afterRemoveLocked()
	h.mu.Unlock()
	sink.Close()
	if h.OnChange != nil {
		h.OnChange(n)
	}
}

// afterRemoveLocked atualiza o contador e para o mixLoop quando ninguém mais ouve.
func (h *spyHub) afterRemoveLocked() int {
	n := len(h.sinks)
	h.n.Store(int32(n))
	if n == 0 && h.stop != nil {
		close(h.stop)
		h.stop = nil
	}
	return n
}

// closeAll fecha todos os espiões (fim da chamada).
func (h *spyHub) closeAll() {
	if h == nil {
		return
	}
	h.mu.Lock()
	sinks := h.sinks
	h.sinks = map[string]spySink{}
	h.n.Store(0)
	if h.stop != nil {
		close(h.stop)
		h.stop = nil
	}
	h.mu.Unlock()
	for _, s := range sinks {
		s.Close()
	}
}

// ids lista os espiões ativos.
func (h *spyHub) ids() []string {
	if h == nil {
		return []string{}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]string, 0, len(h.sinks))
	for id := range h.sinks {
		out = append(out, id)
	}
	return out
}

func (h *spyHub) count() int {
	if h == nil {
		return 0
	}
	return int(h.n.Load())
}

// mixLoop puxa chunks mixados (10ms) e os reenquadra em frames de 960 p/ os sinks
// (mesmo desenho do mixLoop da chamada em grupo).
func (h *spyHub) mixLoop(stop chan struct{}) {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	var framer groupmix.Framer
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			chunk, ok := h.mixer.MixChunk()
			if !ok {
				continue
			}
			frame, full := framer.Push(chunk)
			if !full {
				continue
			}
			h.mu.Lock()
			sinks := make([]spySink, 0, len(h.sinks))
			for _, s := range h.sinks {
				sinks = append(sinks, s)
			}
			h.mu.Unlock()
			for _, s := range sinks {
				_ = s.WritePCM(frame)
			}
		}
	}
}

// spyWebRTC: espião por WebRTC (pion). Reaproveita a Bridge do navegador; só o
// sentido servidor→cliente é usado (Opus 48 kHz na track de áudio).
type spyWebRTC struct {
	br  *Bridge
	enc media.Codec
}

func (s *spyWebRTC) WritePCM(pcm16 []float32) error {
	opus, err := s.enc.Encode(media.Upsample16to48(pcm16))
	if err != nil || len(opus) == 0 {
		return err
	}
	return s.br.WriteOpus(opus, 60*time.Millisecond)
}

func (s *spyWebRTC) Close() {
	s.br.DisableTerminate()
	s.br.Close()
	s.enc.Close()
}

// spyWS: espião por WebSocket (PCM16 LE 16 kHz, só downlink).
type spyWS struct{ b *wsBridge }

func (s *spyWS) WritePCM(pcm16 []float32) error { return s.b.WritePCM(pcm16) }
func (s *spyWS) Close()                         { s.b.Close() }

// handleSpyWebRTC atende POST /api/sessions/{sid}/calls/{id}/spy
// Body: {"sdp_offer": "..."} (áudio recvonly). Resposta: {"sdp_answer", "spy_id"}.
func (s *server) handleSpyWebRTC(w http.ResponseWriter, r *http.Request) {
	sess := s.sessionByID(w, r.PathValue("sid"))
	if sess == nil {
		return
	}
	callID := r.PathValue("id")
	ac, ok := sess.reg.get(callID)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such call"})
		return
	}
	var body struct {
		SDPOffer string `json:"sdp_offer"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.SDPOffer == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "sdp_offer required"})
		return
	}
	enc, err := media.NewOpusCodec(48000, 960)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "opus codec unavailable: " + err.Error()})
		return
	}
	bridge, answer, err := NewBridge(body.SDPOffer, s.log)
	if err != nil {
		enc.Close()
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	sink := &spyWebRTC{br: bridge, enc: enc}
	// a conexão do espião caindo tira SÓ o espião — nunca encerra a chamada.
	bridge.OnTerminalICE = func() { ac.spies.removeSink(sink) }
	id := ac.spies.add(sink)
	s.log.Info("spy: webrtc listener attached", "sid", sess.id, "call", callID, "spy", id, "total", ac.spies.count())
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "spy_id": id, "sdp_answer": answer})
}

// handleSpyWS atende GET /api/sessions/{sid}/calls/{id}/spy/ws — espião por
// WebSocket: só recebe o mix (PCM16 LE 16 kHz); frames enviados são ignorados.
func (s *server) handleSpyWS(w http.ResponseWriter, r *http.Request) {
	sess := s.sessionByID(w, r.PathValue("sid"))
	if sess == nil {
		return
	}
	callID := r.PathValue("id")
	ac, ok := sess.reg.get(callID)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such call"})
		return
	}
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		Subprotocols:       []string{"pcm16"},
		InsecureSkipVerify: true, // CORS já tratado pelo withCORS; origem validada pela API key
	})
	if err != nil {
		s.log.Warn("spy: ws upgrade failed", "err", err, "sid", sess.id, "call", callID)
		return
	}
	b := newWSBridge(conn, s.log, false)
	sink := &spyWS{b: b}
	b.OnTerminalWS = func() { ac.spies.removeSink(sink) }
	id := ac.spies.add(sink)
	s.log.Info("spy: ws listener attached", "sid", sess.id, "call", callID, "spy", id, "total", ac.spies.count())
	go b.keepAlive()
	b.readLoop() // bloqueia até o WS fechar (uplink descartado: OnBrowserPCM nil)
	ac.spies.removeSink(sink)
	s.log.Info("spy: ws listener detached", "sid", sess.id, "call", callID, "spy", id)
}

// handleSpyList atende GET /api/sessions/{sid}/calls/{id}/spy → {count, spies:[ids]}
func (s *server) handleSpyList(w http.ResponseWriter, r *http.Request) {
	sess := s.sessionByID(w, r.PathValue("sid"))
	if sess == nil {
		return
	}
	ac, ok := sess.reg.get(r.PathValue("id"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such call"})
		return
	}
	ids := ac.spies.ids()
	writeJSON(w, http.StatusOK, map[string]any{"count": len(ids), "spies": ids})
}

// handleSpyDelete atende DELETE /api/sessions/{sid}/calls/{id}/spy/{spyId}
func (s *server) handleSpyDelete(w http.ResponseWriter, r *http.Request) {
	sess := s.sessionByID(w, r.PathValue("sid"))
	if sess == nil {
		return
	}
	ac, ok := sess.reg.get(r.PathValue("id"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such call"})
		return
	}
	if !ac.spies.remove(r.PathValue("spyId")) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such spy"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}

// =============================================================================
// Volume da chamada (ganho no áudio do interlocutor → atendente)
// =============================================================================
//
// POST /calls/{id}/volume {"level": 0..3} (1 = normal, 0 = mudo, 2 = dobro).
// O ganho é aplicado NO SERVIDOR ao áudio do WhatsApp antes de sair para o
// consumidor (WebRTC, WebSocket ou SIP), então vale para qualquer cliente —
// painel, widget, widget do AstraChat, agente de IA — sem código no front. A
// gravação e os espiões recebem o áudio original. A mudança é avisada por SSE
// (call-volume) para as UIs sincronizarem o controle.

const (
	volumeMin = 0.0
	volumeMax = 3.0
)

// volumeLevel devolve o ganho atual (1.0 se nunca ajustado).
func (ac *activeCall) volumeLevel() float32 {
	if ac == nil {
		return 1
	}
	v := ac.volumeMilli.Load()
	if v == 0 {
		return 1
	}
	return float32(v-1) / 1000 // armazenado como milli+1 (0 = não ajustado)
}

// setVolume grava o ganho (clamp em [volumeMin, volumeMax]) e devolve o valor aplicado.
func (ac *activeCall) setVolume(level float64) float64 {
	if level < volumeMin || level != level { // NaN
		level = volumeMin
	}
	if level > volumeMax {
		level = volumeMax
	}
	ac.volumeMilli.Store(int32(level*1000) + 1)
	return float64(int32(level*1000)) / 1000
}

// applyGain multiplica o PCM pelo ganho com clamp em [-1,1]. Ganho 1 devolve o
// próprio slice (sem cópia).
func applyGain(pcm []float32, g float32) []float32 {
	if g == 1 || len(pcm) == 0 {
		return pcm
	}
	out := make([]float32, len(pcm))
	for i, v := range pcm {
		v *= g
		if v > 1 {
			v = 1
		} else if v < -1 {
			v = -1
		}
		out[i] = v
	}
	return out
}

// handleCallVolume atende GET/POST /api/sessions/{sid}/calls/{id}/volume
func (s *server) handleCallVolume(w http.ResponseWriter, r *http.Request) {
	sess := s.sessionByID(w, r.PathValue("sid"))
	if sess == nil {
		return
	}
	callID := r.PathValue("id")
	ac, ok := sess.reg.get(callID)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such call"})
		return
	}
	if r.Method == http.MethodGet {
		writeJSON(w, http.StatusOK, map[string]any{"level": float64(ac.volumeLevel())})
		return
	}
	var b struct {
		Level   *flexFloat `json:"level"`
		Percent *flexFloat `json:"percent"`
	}
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	var level float64
	switch {
	case b.Level != nil:
		level = float64(*b.Level)
	case b.Percent != nil:
		level = float64(*b.Percent) / 100
	default:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "level (0..3) or percent (0..300) required"})
		return
	}
	applied := ac.setVolume(level)
	s.broker.emitCallVolume(sess.id, callID, applied)
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "level": applied})
}

// =============================================================================
// Silenciar o toque (só na tela; o celular continua tocando)
// =============================================================================
//
// POST /calls/{id}/silence {"silenced": true|false}. NÃO fala com o WhatsApp:
// a chamada continua tocando no celular e em qualquer outro device. Só marca a
// chamada como silenciada e avisa por SSE (call-action kind=silence) para TODOS
// os widgets/painéis da conta pararem o toque e manterem o pop-up aberto —
// um atendente silencia e os colegas param de ouvir também.

// handleCallSilence atende POST /api/sessions/{sid}/calls/{id}/silence
func (s *server) handleCallSilence(w http.ResponseWriter, r *http.Request) {
	sess := s.sessionByID(w, r.PathValue("sid"))
	if sess == nil {
		return
	}
	callID := r.PathValue("id")
	ac, ok := sess.reg.get(callID)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such call"})
		return
	}
	var b struct {
		Silenced *bool `json:"silenced"`
	}
	_ = json.NewDecoder(r.Body).Decode(&b)
	silenced := true // sem body = silenciar
	if b.Silenced != nil {
		silenced = *b.Silenced
	}
	ac.silenced.Store(silenced)
	s.broker.emitCallAction(sess.id, callID, "silence", silenced)
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "silenced": silenced})
}

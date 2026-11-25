package main

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Música de espera (MoH) própria, por pedido (moh_url no /hold, /transfer,
// /consult) ou por sessão (PUT /api/sessions/{sid}/moh {url}, guardada nas
// opções da sessão). O áudio é baixado uma vez, convertido em PCM 16 kHz mono
// (ffmpeg) e fica em cache por URL. Falha ao baixar/converter cai na música
// padrão: a espera nunca deixa de funcionar por causa da URL.

const (
	mohMaxSeconds = 300 // corta músicas longas (5 min)
	mohCacheMax   = 32
)

type mohEntry struct {
	pcm []float32
	at  time.Time
}

var (
	mohCacheMu sync.Mutex
	mohCache   = map[string]mohEntry{}
	// mohLoader é trocável nos testes.
	mohLoader = loadMOHFromURL
)

func loadMOHFromURL(url string) ([]float32, error) {
	data, err := fetchMedia("", url)
	if err != nil {
		return nil, err
	}
	raw, err := runFFmpeg(data, nil, []string{"-t", "300", "-vn", "-ac", "1", "-ar", "16000", "-f", "s16le"})
	if err != nil {
		return nil, err
	}
	n := len(raw) / 2
	if n == 0 {
		return nil, errors.New("áudio vazio")
	}
	if n > mohMaxSeconds*16000 {
		n = mohMaxSeconds * 16000
	}
	pcm := make([]float32, n)
	for i := 0; i < n; i++ {
		pcm[i] = float32(int16(binary.LittleEndian.Uint16(raw[2*i:]))) / math.MaxInt16
	}
	return pcm, nil
}

// mohPCM devolve a música da URL (cache). Erro = usar a padrão.
func mohPCM(url string) ([]float32, error) {
	url = strings.TrimSpace(url)
	if url == "" {
		return nil, nil
	}
	mohCacheMu.Lock()
	if e, ok := mohCache[url]; ok {
		mohCacheMu.Unlock()
		return e.pcm, nil
	}
	mohCacheMu.Unlock()
	pcm, err := mohLoader(url)
	if err != nil {
		return nil, err
	}
	mohCacheMu.Lock()
	if len(mohCache) >= mohCacheMax { // descarta a mais antiga
		var oldK string
		var oldT time.Time
		for k, e := range mohCache {
			if oldK == "" || e.at.Before(oldT) {
				oldK, oldT = k, e.at
			}
		}
		delete(mohCache, oldK)
	}
	mohCache[url] = mohEntry{pcm: pcm, at: time.Now()}
	mohCacheMu.Unlock()
	return pcm, nil
}

// mohFor resolve a música de espera de um pedido: a URL do pedido, senão a da
// sessão, senão nil (padrão). Nunca falha: erro só vai para o log.
func (s *Session) mohFor(override string) []float32 {
	url := strings.TrimSpace(override)
	if url == "" {
		url = s.getOptions().MOHURL
	}
	if url == "" {
		return nil
	}
	pcm, err := mohPCM(url)
	if err != nil {
		s.log.Warn("música de espera própria indisponível; usando a padrão", "url", url, "err", err)
		return nil
	}
	return pcm
}

// GET /api/sessions/{sid}/moh → {"url": "..."}
func (s *server) handleGetMOH(w http.ResponseWriter, r *http.Request) {
	sess := s.sessionByID(w, r.PathValue("sid"))
	if sess == nil {
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"url": sess.getOptions().MOHURL})
}

// PUT /api/sessions/{sid}/moh {"url": "https://..."} — "" volta para a padrão.
// Baixa e converte na hora para validar (e já deixa em cache).
func (s *server) handleSetMOH(w http.ResponseWriter, r *http.Request) {
	sess := s.sessionByID(w, r.PathValue("sid"))
	if sess == nil {
		return
	}
	var b struct {
		URL string `json:"url"`
	}
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "payload inválido"})
		return
	}
	b.URL = strings.TrimSpace(b.URL)
	seconds := 0.0
	if b.URL != "" {
		pcm, err := mohPCM(b.URL)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "não consegui usar esse áudio: " + err.Error()})
			return
		}
		seconds = float64(len(pcm)) / 16000
	}
	o := sess.getOptions()
	o.MOHURL = b.URL
	if err := saveSessionOptions(r.Context(), sess, o); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"url": b.URL, "seconds": math.Round(seconds*10) / 10})
}

func saveSessionOptions(ctx context.Context, sess *Session, o sessionOptions) error {
	js, _ := json.Marshal(o)
	if sess.mgr.store != nil {
		if err := sess.mgr.store.setOptions(ctx, sess.id, string(js)); err != nil {
			return err
		}
	}
	sess.setOptions(o)
	return nil
}

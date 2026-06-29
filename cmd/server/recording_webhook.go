package main

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Integração de gravação com o EnriqueceAI.
//
// Ao fim de uma chamada, o MP3 mixado é servido em GET /recordings/{id}.mp3
// (rota pública: o id é 16 bytes aleatórios, funciona como capability) e
// notificamos o EnriqueceAI via webhook para que ele baixe e transcreva.
//
// CONTRATO (lado EnriqueceAI — src/app/api/webhooks/wacalls/route.ts):
//   POST {WACALLS_RECORDING_WEBHOOK_URL}
//   Header: X-Webhook-Secret: <WACALLS_WEBHOOK_SECRET>   (mesmo valor nos 2 lados)
//   Body:   { "service_call_id": "<callId>", "recording_url": "https://..." }
//   Respostas: 200 ok | 400 payload inválido | 401 secret inválido | 503 não configurado
//
// Envs:
//   WACALLS_RECORDING_WEBHOOK_URL — endpoint do EnriqueceAI (ex.: https://app.enriqueceai.com.br/api/webhooks/wacalls)
//   WACALLS_WEBHOOK_SECRET        — segredo compartilhado do header
//   WACALLS_PUBLIC_BASE_URL       — base pública deste serviço (ex.: https://voice.v4companyamaral.com)
//   WACALLS_RECORDING_DIR         — dir dos MP3s (default: $TMPDIR/wacalls-recordings)

var recordingWebhookClient = &http.Client{Timeout: 15 * time.Second}

// recordingPublicURL monta a URL pública de download a partir do path do arquivo.
// Retorna "" se a base pública não estiver configurada.
func recordingPublicURL(path string) string {
	base := strings.TrimRight(os.Getenv("WACALLS_PUBLIC_BASE_URL"), "/")
	if base == "" {
		return ""
	}
	return base + "/recordings/" + filepath.Base(path)
}

// dispatchRecordingWebhook notifica o EnriqueceAI que a gravação está pronta.
// Fire-and-forget com retry/backoff; falhas são apenas logadas (o cron
// recover-missing-recordings do app é a rede de segurança).
func dispatchRecordingWebhook(log *slog.Logger, callID, recordingURL string) {
	url := os.Getenv("WACALLS_RECORDING_WEBHOOK_URL")
	secret := os.Getenv("WACALLS_WEBHOOK_SECRET")
	if url == "" || secret == "" {
		log.Debug("recording webhook skipped: url/secret not configured", "call_id", callID)
		return
	}
	body, err := json.Marshal(map[string]string{
		"service_call_id": callID,
		"recording_url":   recordingURL,
	})
	if err != nil {
		return
	}

	go func() {
		// Backoff curto: a gravação fica pronta antes de o SDR concluir o modal,
		// então há folga; ainda assim toleramos indisponibilidade momentânea.
		backoffs := []time.Duration{0, 3 * time.Second, 15 * time.Second}
		for attempt, wait := range backoffs {
			if wait > 0 {
				time.Sleep(wait)
			}
			req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, url, bytes.NewReader(body))
			if err != nil {
				return
			}
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-Webhook-Secret", secret)
			resp, err := recordingWebhookClient.Do(req)
			if err != nil {
				log.Debug("recording webhook post failed", "call_id", callID, "attempt", attempt, "err", err)
				continue
			}
			status := resp.StatusCode
			_ = resp.Body.Close()
			if status >= 200 && status < 300 {
				log.Info("recording webhook delivered", "call_id", callID, "status", status)
				return
			}
			// 4xx é erro de payload/credencial — reenviar não adianta.
			if status >= 400 && status < 500 {
				log.Warn("recording webhook rejected", "call_id", callID, "status", status)
				return
			}
			log.Debug("recording webhook non-2xx", "call_id", callID, "status", status, "attempt", attempt)
		}
		log.Warn("recording webhook gave up after retries", "call_id", callID)
	}()
}

// handleRecording serve um MP3 de gravação finalizado. Rota pública (fora de
// /api/, então sem API key) — o id é não-enumerável e funciona como capability.
func (s *server) handleRecording(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !safeRecordingID(id) {
		http.NotFound(w, r)
		return
	}
	full := filepath.Join(recordingDir(), filepath.Base(id))
	if _, err := os.Stat(full); err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "audio/mpeg")
	http.ServeFile(w, r, full)
}

// safeRecordingID barra path traversal: só nome simples [A-Za-z0-9._-], sem
// separador de caminho nem "..".
func safeRecordingID(id string) bool {
	if id == "" || id == "." || id == ".." || strings.Contains(id, "..") {
		return false
	}
	for _, c := range id {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '.' || c == '_' || c == '-':
		default:
			return false
		}
	}
	return true
}

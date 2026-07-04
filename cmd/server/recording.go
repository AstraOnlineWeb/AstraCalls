package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"go.mau.fi/whatsmeow/types"
)

// Entrega da gravação: ao fim da chamada o MP3 mixado é (1) servido em
// GET /recordings/{id} (rota pública — o id/callID é não-enumerável e funciona
// como capability), (2) anexado no Chatwoot como NOTA PRIVADA na conversa do
// número ligado (fica interno pros agentes, nunca vai pro cliente) e (3)
// anunciado no webhook da sessão como evento "recording". Ligar/desligar é
// por sessão (Session.recording).

// recordingPublicURL monta a URL pública de download a partir do path do arquivo.
// Retorna "" se WACALLS_PUBLIC_BASE_URL não estiver configurada.
func recordingPublicURL(path string) string {
	base := strings.TrimRight(os.Getenv("WACALLS_PUBLIC_BASE_URL"), "/")
	if base == "" {
		return ""
	}
	return base + "/recordings/" + filepath.Base(path)
}

// onRecordingReady é chamado quando o MP3 de uma chamada fica pronto. Dispara o
// webhook da sessão e sobe o áudio no Chatwoot (se configurado).
func (s *Session) onRecordingReady(callID, peerJID, path string, seconds int) {
	url := recordingPublicURL(path)
	s.dispatchWebhook("recording", map[string]any{
		"callId":  callID,
		"to":      peerJID,
		"url":     url,
		"seconds": seconds,
	})
	if cfg := s.getChatwoot(); cfg.valid() && peerJID != "" {
		s.chatwootUploadRecording(cfg, peerJID, path, seconds)
	}
}

// chatwootUploadRecording anexa o MP3 na conversa do número ligado como nota
// privada (não é reenviado ao cliente).
func (s *Session) chatwootUploadRecording(cfg ChatwootConfig, peerJID, path string, seconds int) {
	jid, err := resolveRecipient(peerJID)
	if err != nil {
		return
	}
	phone := s.realPhone(jid)
	if phone == "" {
		phone = jid.User
	}
	if phone == "" {
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		s.log.Error("recording: ler mp3 falhou", "err", err)
		return
	}
	chatID := phone + "@" + types.DefaultUserServer
	contactID, sourceID, err := cfg.ensureContact(chatID, phone, phone, "")
	if err != nil {
		s.log.Error("recording: ensure contact falhou", "err", err)
		return
	}
	convID, err := cfg.ensureConversation(contactID, sourceID)
	if err != nil {
		s.log.Error("recording: ensure conversation falhou", "err", err)
		return
	}
	caption := "🎙️ Gravação da chamada · " + fmtDuration(seconds)
	if err := cfg.postAttachment(convID, caption, filepath.Base(path), "audio/mpeg", data, true, "", ""); err != nil {
		s.log.Error("recording: upload no chatwoot falhou", "err", err)
	}
}

func fmtDuration(seconds int) string {
	return fmt.Sprintf("%d:%02d", seconds/60, seconds%60)
}

// handleRecording serve um MP3 finalizado. Rota pública (fora de /api/, sem API
// key) — o id é não-enumerável e funciona como capability.
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

// safeRecordingID barra path traversal: só nome simples [A-Za-z0-9._-].
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

// GET /api/sessions/{sid}/recording → estado do toggle da sessão
func (s *server) handleGetRecording(w http.ResponseWriter, r *http.Request) {
	sess := s.sessionByID(w, r.PathValue("sid"))
	if sess == nil {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"enabled": sess.getRecording()})
}

// PUT /api/sessions/{sid}/recording {enabled: bool} → liga/desliga a gravação
func (s *server) handleSetRecording(w http.ResponseWriter, r *http.Request) {
	sess := s.sessionByID(w, r.PathValue("sid"))
	if sess == nil {
		return
	}
	var b struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "enabled required"})
		return
	}
	sess.setRecording(b.Enabled)
	_ = sess.mgr.store.setRecording(r.Context(), sess.id, b.Enabled)
	sess.mgr.broker.emitSessionList(sess.mgr.infos())
	writeJSON(w, http.StatusOK, map[string]any{"enabled": b.Enabled})
}

// startRecordingJanitor apaga MP3s antigos periodicamente (retenção ~48h) — o
// Chatwoot/webhook já consumiram o áudio; o arquivo local é só uma ponte.
func startRecordingJanitor(log *slog.Logger) {
	go func() {
		for {
			cleanupOldRecordings(log)
			time.Sleep(6 * time.Hour)
		}
	}()
}

func cleanupOldRecordings(log *slog.Logger) {
	dir := recordingDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-48 * time.Hour)
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.ModTime().Before(cutoff) {
			if err := os.Remove(filepath.Join(dir, e.Name())); err == nil {
				log.Debug("recording antiga removida", "file", e.Name())
			}
		}
	}
}

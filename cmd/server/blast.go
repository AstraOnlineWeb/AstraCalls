package main

import (
	"context"
	"encoding/json"
	"math/rand"
	"net/http"
	"strings"
	"sync"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"google.golang.org/protobuf/proto"
)

// Disparo (blast) com pacing anti-ban. Enfileira e responde na hora; um worker
// sequencial ÚNICO por job envia com delay + jitter aleatório entre mensagens,
// retry por destinatário, e cancelamento cooperativo. Estado em memória (some no
// restart) — suficiente pra campanhas via Chatwoot/n8n. Ideia inspirada no waxum.
const (
	blastDefaultDelayMs = 3000
	blastMaxAttempts    = 3
	blastDLQCap         = 500
)

type blastResult struct {
	To    string `json:"to"`
	OK    bool   `json:"ok"`
	MsgID string `json:"msgId,omitempty"`
	Error string `json:"error,omitempty"`
}

type blastJob struct {
	ID        string        `json:"id"`
	SessionID string        `json:"sessionId"`
	Status    string        `json:"status"` // pending|running|completed|completed_with_failures|canceled
	Total     int           `json:"total"`
	Sent      int           `json:"sent"`
	Failed    int           `json:"failed"`
	CreatedAt int64         `json:"createdAt"`
	Results   []blastResult `json:"results,omitempty"`
	cancel    chan struct{} `json:"-"`
}

type blastManager struct {
	mu   sync.Mutex
	jobs map[string]*blastJob // key: jobID
}

var blasts = &blastManager{jobs: map[string]*blastJob{}}

// start cria o job e dispara o worker. msg é o conteúdo (mesmo p/ todos). O
// upload de mídia (se houver) já foi feito pelo chamador.
func (m *blastManager) start(sess *Session, recipients []string, msg *waE2E.Message, delayMs, jitterMs int) *blastJob {
	job := &blastJob{
		ID: whNewID(), SessionID: sess.id, Status: "pending",
		Total: len(recipients), CreatedAt: time.Now().UnixMilli(),
		cancel: make(chan struct{}),
	}
	m.mu.Lock()
	m.jobs[job.ID] = job
	// limita o histórico em memória
	if len(m.jobs) > blastDLQCap {
		var oldest *blastJob
		for _, j := range m.jobs {
			if oldest == nil || j.CreatedAt < oldest.CreatedAt {
				oldest = j
			}
		}
		if oldest != nil {
			delete(m.jobs, oldest.ID)
		}
	}
	m.mu.Unlock()

	go m.run(sess, job, recipients, msg, delayMs, jitterMs)
	return job
}

func (m *blastManager) run(sess *Session, job *blastJob, recipients []string, msg *waE2E.Message, delayMs, jitterMs int) {
	m.setStatus(job, "running")
	for i, to := range recipients {
		select {
		case <-job.cancel:
			m.setStatus(job, "canceled")
			return
		default:
		}
		if i > 0 {
			d := delayMs
			if jitterMs > 0 {
				d += rand.Intn(jitterMs + 1)
			}
			select {
			case <-job.cancel:
				m.setStatus(job, "canceled")
				return
			case <-time.After(time.Duration(d) * time.Millisecond):
			}
		}
		res := m.sendOne(sess, to, msg)
		m.mu.Lock()
		job.Results = append(job.Results, res)
		if res.OK {
			job.Sent++
		} else {
			job.Failed++
		}
		m.mu.Unlock()
	}
	if job.Failed > 0 {
		m.setStatus(job, "completed_with_failures")
	} else {
		m.setStatus(job, "completed")
	}
}

func (m *blastManager) sendOne(sess *Session, to string, msg *waE2E.Message) blastResult {
	jid, err := resolveRecipient(to)
	if err != nil {
		return blastResult{To: to, Error: err.Error()}
	}
	var lastErr string
	for attempt := 0; attempt < blastMaxAttempts; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		id, e := sess.sendAndMark(ctx, jid, msg) // sendAndMark tem o hardening de LID
		cancel()
		if e == nil {
			return blastResult{To: to, OK: true, MsgID: id}
		}
		lastErr = e.Error()
		time.Sleep(2 * time.Second)
	}
	return blastResult{To: to, Error: lastErr}
}

func (m *blastManager) setStatus(job *blastJob, s string) {
	m.mu.Lock()
	job.Status = s
	m.mu.Unlock()
}

func (m *blastManager) get(id string) *blastJob {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.jobs[id]
}

func (m *blastManager) list(sessionID string) []*blastJob {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []*blastJob
	for _, j := range m.jobs {
		if j.SessionID == sessionID {
			out = append(out, j)
		}
	}
	return out
}

func (m *blastManager) cancelJob(id string) bool {
	m.mu.Lock()
	j := m.jobs[id]
	m.mu.Unlock()
	if j == nil {
		return false
	}
	select {
	case <-j.cancel: // já cancelado
	default:
		close(j.cancel)
	}
	return true
}

// snapshot devolve uma cópia estável do job (sem o canal) sob lock.
func (m *blastManager) snapshot(j *blastJob, withResults bool) map[string]any {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[string]any{
		"id": j.ID, "sessionId": j.SessionID, "status": j.Status,
		"total": j.Total, "sent": j.Sent, "failed": j.Failed, "createdAt": j.CreatedAt,
	}
	if withResults {
		rc := make([]blastResult, len(j.Results))
		copy(rc, j.Results)
		out["results"] = rc
	}
	return out
}

// ---------- endpoints ----------

// POST /api/sessions/{sid}/blast
func (s *server) handleBlast(w http.ResponseWriter, r *http.Request) {
	sess := s.pairedSession(w, r.PathValue("sid"))
	if sess == nil {
		return
	}
	var b struct {
		To       []string `json:"to"`
		Text     string   `json:"text"`
		ImageURL string   `json:"imageUrl"`
		Base64   string   `json:"base64"`
		Caption  string   `json:"caption"`
		DelayMs  int      `json:"delayMs"`
		JitterMs int      `json:"jitterMs"`
	}
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil || len(b.To) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "to (lista) obrigatório"})
		return
	}
	hasMedia := strings.TrimSpace(b.ImageURL) != "" || strings.TrimSpace(b.Base64) != ""
	if strings.TrimSpace(b.Text) == "" && !hasMedia {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "text ou imagem (imageUrl/base64) obrigatório"})
		return
	}
	var msg *waE2E.Message
	if hasMedia {
		// upload UMA vez; a mesma mídia é enviada a todos.
		up, ok := s.uploadMedia(sess, w, r, b.Base64, b.ImageURL, whatsmeow.MediaImage)
		if !ok {
			return // uploadMedia já respondeu o erro
		}
		msg = &waE2E.Message{ImageMessage: &waE2E.ImageMessage{
			Caption: proto.String(firstNonEmptyOf(b.Caption, b.Text)), Mimetype: proto.String("image/jpeg"),
			URL: &up.URL, DirectPath: &up.DirectPath, MediaKey: up.MediaKey,
			FileEncSHA256: up.FileEncSHA256, FileSHA256: up.FileSHA256, FileLength: proto.Uint64(up.FileLength),
		}}
	} else {
		msg = &waE2E.Message{Conversation: proto.String(b.Text)}
	}
	delay := b.DelayMs
	if delay <= 0 {
		delay = blastDefaultDelayMs
	}
	job := blasts.start(sess, b.To, msg, delay, b.JitterMs)
	writeJSON(w, http.StatusOK, map[string]any{"jobId": job.ID, "status": job.Status, "total": job.Total})
}

// GET /api/sessions/{sid}/blasts
func (s *server) handleBlastList(w http.ResponseWriter, r *http.Request) {
	sess := s.sessionByID(w, r.PathValue("sid"))
	if sess == nil {
		return
	}
	jobs := blasts.list(sess.id)
	out := make([]map[string]any, 0, len(jobs))
	for _, j := range jobs {
		out = append(out, blasts.snapshot(j, false))
	}
	writeJSON(w, http.StatusOK, map[string]any{"jobs": out})
}

// GET /api/sessions/{sid}/blasts/{id}
func (s *server) handleBlastGet(w http.ResponseWriter, r *http.Request) {
	sess := s.sessionByID(w, r.PathValue("sid"))
	if sess == nil {
		return
	}
	j := blasts.get(r.PathValue("id"))
	if j == nil || j.SessionID != sess.id {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "job não encontrado"})
		return
	}
	writeJSON(w, http.StatusOK, blasts.snapshot(j, true))
}

// POST /api/sessions/{sid}/blasts/{id}/cancel
func (s *server) handleBlastCancel(w http.ResponseWriter, r *http.Request) {
	sess := s.sessionByID(w, r.PathValue("sid"))
	if sess == nil {
		return
	}
	j := blasts.get(r.PathValue("id"))
	if j == nil || j.SessionID != sess.id {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "job não encontrado"})
		return
	}
	blasts.cancelJob(j.ID)
	writeJSON(w, http.StatusOK, map[string]string{"status": "canceling"})
}

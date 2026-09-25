package main

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

var mediaHTTP = &http.Client{Timeout: 30 * time.Second}

// pairedSession devolve a sessão se existir E estiver pareada, ou escreve o erro.
func (s *server) pairedSession(w http.ResponseWriter, sid string) *Session {
	sess := s.sessionByID(w, sid)
	if sess == nil {
		return nil
	}
	if sess.client.Store.ID == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "not paired"})
		return nil
	}
	return sess
}

// resolveRecipient aceita um número (DDI+DDD+num) ou um JID completo (com @).
func resolveRecipient(to string) (types.JID, error) {
	to = strings.TrimSpace(to)
	if to == "" {
		return types.JID{}, errors.New("recipient required")
	}
	if strings.Contains(to, "@") {
		return types.ParseJID(to)
	}
	return types.NewJID(normalizePhone(to), types.DefaultUserServer), nil
}

// fetchMedia obtém os bytes da mídia a partir de base64 (data) ou de uma URL.
func fetchMedia(b64, url string) ([]byte, error) {
	data, _, err := fetchMediaWithType(b64, url, true) // url do usuário -> com guarda SSRF
	return data, err
}

// fetchMediaWithType é como fetchMedia, mas também devolve o Content-Type que o
// servidor de origem informou (útil p/ documentos: o active_storage do Chatwoot
// responde "application/pdf" mesmo quando o data_url não tem extensão, evitando
// que o arquivo chegue como ".bin" no WhatsApp Android). Devolve "" quando não há
// header (ex.: origem base64).
// guarded=true aplica a guarda anti-SSRF (para URLs vindas do usuário via
// /messages/*); false pula a guarda (para o data_url do Chatwoot, que pode ser
// um host interno legítimo).
func fetchMediaWithType(b64, url string, guarded bool) ([]byte, string, error) {
	if b64 != "" {
		if strings.HasPrefix(b64, "data:") {
			ct := ""
			if i := strings.Index(b64, ","); i > 0 {
				if meta := b64[len("data:"):i]; meta != "" {
					ct = strings.TrimSuffix(meta, ";base64")
				}
				b64 = b64[i+1:]
			}
			data, err := base64.StdEncoding.DecodeString(strings.TrimSpace(b64))
			return data, ct, err
		}
		data, err := base64.StdEncoding.DecodeString(strings.TrimSpace(b64))
		return data, "", err
	}
	if url != "" {
		client := mediaHTTP
		if guarded {
			if err := validateUserURL(url); err != nil {
				return nil, "", err
			}
			client = guardedMediaHTTP // valida o IP no dial (anti-SSRF/rebinding)
		}
		resp, err := client.Get(url)
		if err != nil {
			return nil, "", err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, "", errors.New("download failed: " + resp.Status)
		}
		ct := resp.Header.Get("Content-Type")
		if i := strings.IndexByte(ct, ';'); i >= 0 { // remove "; charset=..."
			ct = ct[:i]
		}
		data, err := io.ReadAll(io.LimitReader(resp.Body, 100<<20)) // teto de 100MB
		return data, strings.TrimSpace(ct), err
	}
	return nil, "", errors.New("base64 or url required")
}

func (s *server) send(sess *Session, w http.ResponseWriter, r *http.Request, to string, msg *waE2E.Message) {
	jid, err := resolveRecipient(to)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	s.sendTo(sess, w, r, jid, msg)
}

// uploadFor faz o download/decode + upload da mídia, retornando a mensagem montada
// pela função builder. Centraliza o tratamento de erro de mídia.
func (s *server) uploadMedia(sess *Session, w http.ResponseWriter, r *http.Request, b64, url string, mt whatsmeow.MediaType) (*whatsmeow.UploadResponse, bool) {
	data, err := fetchMedia(b64, url)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return nil, false
	}
	up, err := sess.client.Upload(r.Context(), data, mt)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return nil, false
	}
	return &up, true
}

// ---- Handlers de envio ----

// GET /api/sessions/{sid}/messages/new-message-id → devolve um id de mensagem
// pré-gerado. Use no envio (header X-Message-ID ou ?id=) para IDEMPOTÊNCIA: se o
// envio for repetido (timeout/retry) com o mesmo id, o WhatsApp não duplica.
func (s *server) handleNewMessageID(w http.ResponseWriter, r *http.Request) {
	sess := s.pairedSession(w, r.PathValue("sid"))
	if sess == nil {
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": string(sess.client.GenerateMessageID())})
}

// handleForward encaminha uma mensagem já existente (recebida ou enviada) para
// outro destino. Reconstrói a mensagem a partir do proto cru guardado no histórico
// (reusa o mesmo ponteiro/mediaKey no caso de mídia) e a marca como encaminhada.
// POST /api/sessions/{sid}/messages/forward {to, messageId}
func (s *server) handleForward(w http.ResponseWriter, r *http.Request) {
	sess := s.pairedSession(w, r.PathValue("sid"))
	if sess == nil {
		return
	}
	var b struct {
		To        string `json:"to"`
		MessageID string `json:"messageId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil || strings.TrimSpace(b.To) == "" || strings.TrimSpace(b.MessageID) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "to e messageId obrigatórios"})
		return
	}
	_, _, _, raw, err := sess.mgr.store.findMessage(r.Context(), sess.id, b.MessageID)
	if err != nil || len(raw) == 0 {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "mensagem não encontrada no histórico"})
		return
	}
	var msg waE2E.Message
	if err := protojson.Unmarshal(raw, &msg); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "falha ao reconstruir a mensagem: " + err.Error()})
		return
	}
	// marca como encaminhada (label "Encaminhada" no WhatsApp)
	applyContextInfo(&msg, &waE2E.ContextInfo{IsForwarded: proto.Bool(true), ForwardingScore: proto.Uint32(1)})
	s.send(sess, w, r, b.To, &msg)
}

func (s *server) handleSendText(w http.ResponseWriter, r *http.Request) {
	sess := s.pairedSession(w, r.PathValue("sid"))
	if sess == nil {
		return
	}
	var b struct {
		To              string   `json:"to"`
		Text            string   `json:"text"`
		QuotedMessageID string   `json:"quotedMessageId"` // citar (reply) a msg com esse id
		Participant     string   `json:"participant"`     // em grupo: quem mandou a citada
		FromMe          bool     `json:"fromMe"`          // a citada é nossa
		Mentions        []string `json:"mentions"`        // JIDs/números a mencionar (@)
	}
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil || strings.TrimSpace(b.Text) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "to and text required"})
		return
	}
	msg := &waE2E.Message{Conversation: proto.String(b.Text)}
	applyContextInfo(msg, sess.buildSendContext(r.Context(), b.QuotedMessageID, b.Participant, b.FromMe, b.Mentions))
	s.send(sess, w, r, b.To, msg)
}

func (s *server) handleSendImage(w http.ResponseWriter, r *http.Request) {
	sess := s.pairedSession(w, r.PathValue("sid"))
	if sess == nil {
		return
	}
	var b struct {
		To, Base64, URL, Caption, Mimetype string
		QuotedMessageID, Participant       string
		FromMe                             bool
		Mentions                           []string
	}
	_ = json.NewDecoder(r.Body).Decode(&b)
	up, ok := s.uploadMedia(sess, w, r, b.Base64, b.URL, whatsmeow.MediaImage)
	if !ok {
		return
	}
	mime := b.Mimetype
	if mime == "" {
		mime = "image/jpeg"
	}
	msg := &waE2E.Message{ImageMessage: &waE2E.ImageMessage{
		Caption: proto.String(b.Caption), Mimetype: proto.String(mime),
		URL: &up.URL, DirectPath: &up.DirectPath, MediaKey: up.MediaKey,
		FileEncSHA256: up.FileEncSHA256, FileSHA256: up.FileSHA256, FileLength: proto.Uint64(up.FileLength),
	}}
	applyContextInfo(msg, sess.buildSendContext(r.Context(), b.QuotedMessageID, b.Participant, b.FromMe, b.Mentions))
	s.send(sess, w, r, b.To, msg)
}

func (s *server) handleSendAudio(w http.ResponseWriter, r *http.Request) {
	sess := s.pairedSession(w, r.PathValue("sid"))
	if sess == nil {
		return
	}
	var b struct {
		To, Base64, URL, Mimetype    string
		PTT                          bool
		Seconds                      int    // duração (s); se 0 e PTT, calcula do OGG
		Waveform                     string // 64 bytes em base64; se vazio e PTT, gera
		QuotedMessageID, Participant string
		FromMe                       bool
		Mentions                     []string
	}
	_ = json.NewDecoder(r.Body).Decode(&b)
	// Buscamos os bytes primeiro (em vez de uploadMedia) p/ poder estimar a duração
	// do OGG quando o cliente não informa `seconds`.
	data, err := fetchMedia(b.Base64, b.URL)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	up, err := sess.client.Upload(r.Context(), data, whatsmeow.MediaAudio)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	mime := b.Mimetype
	if mime == "" {
		mime = "audio/ogg; codecs=opus"
	}
	am := &waE2E.AudioMessage{
		Mimetype: proto.String(mime), PTT: proto.Bool(b.PTT),
		URL: &up.URL, DirectPath: &up.DirectPath, MediaKey: up.MediaKey,
		FileEncSHA256: up.FileEncSHA256, FileSHA256: up.FileSHA256, FileLength: proto.Uint64(up.FileLength),
	}
	// Duração: usa a informada; senão (nota de voz) estima pelo OGG/Opus.
	secs := uint32(0)
	if b.Seconds > 0 {
		secs = uint32(b.Seconds)
	} else if b.PTT {
		secs = oggOpusDurationSeconds(data)
	}
	if secs > 0 {
		am.Seconds = proto.Uint32(secs)
	}
	// Waveform: usa o real enviado (64 bytes base64); senão, numa nota de voz, gera
	// um aproximado p/ não ficar barra reta no destinatário.
	if wf, derr := base64.StdEncoding.DecodeString(strings.TrimSpace(b.Waveform)); derr == nil && len(wf) == 64 {
		am.Waveform = wf
	} else if b.PTT {
		am.Waveform = synthWaveform()
	}
	msg := &waE2E.Message{AudioMessage: am}
	applyContextInfo(msg, sess.buildSendContext(r.Context(), b.QuotedMessageID, b.Participant, b.FromMe, b.Mentions))
	s.send(sess, w, r, b.To, msg)
}

func (s *server) handleSendVideo(w http.ResponseWriter, r *http.Request) {
	sess := s.pairedSession(w, r.PathValue("sid"))
	if sess == nil {
		return
	}
	var b struct {
		To, Base64, URL, Caption, Mimetype string
		QuotedMessageID, Participant       string
		FromMe                             bool
		Mentions                           []string
	}
	_ = json.NewDecoder(r.Body).Decode(&b)
	up, ok := s.uploadMedia(sess, w, r, b.Base64, b.URL, whatsmeow.MediaVideo)
	if !ok {
		return
	}
	mime := b.Mimetype
	if mime == "" {
		mime = "video/mp4"
	}
	msg := &waE2E.Message{VideoMessage: &waE2E.VideoMessage{
		Caption: proto.String(b.Caption), Mimetype: proto.String(mime),
		URL: &up.URL, DirectPath: &up.DirectPath, MediaKey: up.MediaKey,
		FileEncSHA256: up.FileEncSHA256, FileSHA256: up.FileSHA256, FileLength: proto.Uint64(up.FileLength),
	}}
	applyContextInfo(msg, sess.buildSendContext(r.Context(), b.QuotedMessageID, b.Participant, b.FromMe, b.Mentions))
	s.send(sess, w, r, b.To, msg)
}

// handleSendPtv envia uma NOTA DE VÍDEO (PTV — o "balãozinho" de vídeo redondo).
// É um VideoMessage colocado no campo ptvMessage; o WhatsApp exibe redondo. Ideal:
// vídeo curto (<60s) e quadrado. Não leva legenda (o app não mostra).
func (s *server) handleSendPtv(w http.ResponseWriter, r *http.Request) {
	sess := s.pairedSession(w, r.PathValue("sid"))
	if sess == nil {
		return
	}
	var b struct {
		To, Base64, URL, Mimetype    string
		QuotedMessageID, Participant string
		FromMe                       bool
	}
	_ = json.NewDecoder(r.Body).Decode(&b)
	up, ok := s.uploadMedia(sess, w, r, b.Base64, b.URL, whatsmeow.MediaVideo)
	if !ok {
		return
	}
	mime := b.Mimetype
	if mime == "" {
		mime = "video/mp4"
	}
	msg := &waE2E.Message{PtvMessage: &waE2E.VideoMessage{
		Mimetype: proto.String(mime),
		URL:      &up.URL, DirectPath: &up.DirectPath, MediaKey: up.MediaKey,
		FileEncSHA256: up.FileEncSHA256, FileSHA256: up.FileSHA256, FileLength: proto.Uint64(up.FileLength),
	}}
	applyContextInfo(msg, sess.buildSendContext(r.Context(), b.QuotedMessageID, b.Participant, b.FromMe, nil))
	s.send(sess, w, r, b.To, msg)
}

// documentWithCaption monta a mensagem de documento, embrulhando-a em
// documentWithCaptionMessage quando há legenda. É o formato que o WhatsApp oficial
// usa para exibir a legenda do arquivo; um documentMessage.Caption "solto" muitas
// vezes não aparece no destino. Sem legenda, envia o documentMessage puro.
func documentWithCaption(doc *waE2E.DocumentMessage, caption string) *waE2E.Message {
	if caption != "" {
		doc.Caption = proto.String(caption)
		return &waE2E.Message{DocumentWithCaptionMessage: &waE2E.FutureProofMessage{
			Message: &waE2E.Message{DocumentMessage: doc},
		}}
	}
	return &waE2E.Message{DocumentMessage: doc}
}

func (s *server) handleSendDocument(w http.ResponseWriter, r *http.Request) {
	sess := s.pairedSession(w, r.PathValue("sid"))
	if sess == nil {
		return
	}
	var b struct {
		To, Base64, URL, FileName, Mimetype, Caption string
		QuotedMessageID, Participant                 string
		FromMe                                       bool
		Mentions                                     []string
	}
	_ = json.NewDecoder(r.Body).Decode(&b)
	up, ok := s.uploadMedia(sess, w, r, b.Base64, b.URL, whatsmeow.MediaDocument)
	if !ok {
		return
	}
	// Sem mimetype (ou genérico), deduz pela extensão do nome — senão o WhatsApp
	// Android exibe o documento como ".bin".
	mime := b.Mimetype
	if isGenericMime(mime) {
		mime = firstNonEmptyOf(mimeByFileName(b.FileName), "application/octet-stream")
	}
	name := b.FileName
	if name == "" {
		name = "file"
	}
	name = ensureFileExt(name, mime)
	doc := &waE2E.DocumentMessage{
		FileName: proto.String(name), Title: proto.String(name), Mimetype: proto.String(mime),
		URL: &up.URL, DirectPath: &up.DirectPath, MediaKey: up.MediaKey,
		FileEncSHA256: up.FileEncSHA256, FileSHA256: up.FileSHA256, FileLength: proto.Uint64(up.FileLength),
	}
	// citação/menção vão no ContextInfo do documento interno (o wrapper de legenda
	// só embrulha essa mesma mensagem).
	doc.ContextInfo = sess.buildSendContext(r.Context(), b.QuotedMessageID, b.Participant, b.FromMe, b.Mentions)
	s.send(sess, w, r, b.To, documentWithCaption(doc, b.Caption))
}

func (s *server) handleSendSticker(w http.ResponseWriter, r *http.Request) {
	sess := s.pairedSession(w, r.PathValue("sid"))
	if sess == nil {
		return
	}
	var b struct {
		To, Base64, URL, Mimetype string
		Animated                  bool
	}
	_ = json.NewDecoder(r.Body).Decode(&b)
	// Figurinha precisa ser WebP pronto (512x512). Não convertemos aqui.
	up, ok := s.uploadMedia(sess, w, r, b.Base64, b.URL, whatsmeow.MediaImage)
	if !ok {
		return
	}
	mime := b.Mimetype
	if mime == "" {
		mime = "image/webp"
	}
	s.send(sess, w, r, b.To, &waE2E.Message{StickerMessage: &waE2E.StickerMessage{
		Mimetype: proto.String(mime), IsAnimated: proto.Bool(b.Animated),
		URL: &up.URL, DirectPath: &up.DirectPath, MediaKey: up.MediaKey,
		FileEncSHA256: up.FileEncSHA256, FileSHA256: up.FileSHA256, FileLength: proto.Uint64(up.FileLength),
	}})
}

// ---- Handlers de configuração do webhook ----

// splitEvents divide um CSV de tipos de evento em lista, ignorando vazios/espaços.
func splitEvents(csv string) []string {
	if strings.TrimSpace(csv) == "" {
		return nil
	}
	parts := strings.Split(csv, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if e := strings.TrimSpace(p); e != "" {
			out = append(out, e)
		}
	}
	return out
}

func (s *server) handleSetWebhook(w http.ResponseWriter, r *http.Request) {
	sess := s.sessionByID(w, r.PathValue("sid"))
	if sess == nil {
		return
	}
	var b struct {
		URL    string   `json:"url"`
		Secret string   `json:"secret"` // opcional: assina o payload (X-Webhook-Signature: sha256=…)
		Events []string `json:"events"` // opcional: só entrega estes tipos; vazio = todos
	}
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "url required"})
		return
	}
	url := strings.TrimSpace(b.URL)
	secret := strings.TrimSpace(b.Secret)
	events := make([]string, 0, len(b.Events))
	for _, e := range b.Events {
		if e = strings.TrimSpace(e); e != "" {
			events = append(events, e)
		}
	}
	sess.setWebhook(url, secret, events)
	_ = sess.mgr.store.setWebhook(r.Context(), sess.id, url, secret, strings.Join(events, ","))
	writeJSON(w, http.StatusOK, map[string]any{
		"webhook": url, "hasSecret": secret != "", "events": events,
	})
}

func (s *server) handleGetWebhook(w http.ResponseWriter, r *http.Request) {
	sess := s.sessionByID(w, r.PathValue("sid"))
	if sess == nil {
		return
	}
	// não devolve o secret em claro; só indica se está configurado.
	writeJSON(w, http.StatusOK, map[string]any{
		"webhook": sess.getWebhook(), "hasSecret": sess.getWebhookSecret() != "", "events": sess.getWebhookEvents(),
	})
}

func (s *server) handleDeleteWebhook(w http.ResponseWriter, r *http.Request) {
	sess := s.sessionByID(w, r.PathValue("sid"))
	if sess == nil {
		return
	}
	sess.setWebhook("", "", nil)
	_ = sess.mgr.store.setWebhook(r.Context(), sess.id, "", "", "")
	w.WriteHeader(http.StatusNoContent)
}

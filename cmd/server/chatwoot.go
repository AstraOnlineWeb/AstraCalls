package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"mime/multipart"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

// Integração com Chatwoot (canal API), inspirada no app chatwoot do WAHA.
// Mapeia o contato Chatwoot <-> chat do WhatsApp via custom attribute.

const cwChatIDAttr = "wacalls_chat_id"

func isGroupChatID(id string) bool {
	return strings.HasSuffix(id, "@g.us")
}

type ChatwootConfig struct {
	URL             string `json:"url"`
	AccountID       int    `json:"account_id"`
	AccountToken    string `json:"account_token"`
	InboxID         int    `json:"inbox_id"`
	InboxIdentifier string `json:"inbox_identifier"`
}

func (c ChatwootConfig) valid() bool {
	return c.URL != "" && c.AccountID != 0 && c.AccountToken != "" && c.InboxID != 0
}

func (c ChatwootConfig) base() string {
	return strings.TrimRight(c.URL, "/") + "/api/v1/accounts/" + strconv.Itoa(c.AccountID)
}

var cwHTTP = &http.Client{Timeout: 30 * time.Second}

// cwReq faz uma chamada JSON na Application API do Chatwoot.
func (c ChatwootConfig) req(method, path string, body any) (map[string]any, int, error) {
	var rdr io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, c.base()+path, rdr)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("api_access_token", c.AccountToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := cwHTTP.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	var out map[string]any
	_ = json.Unmarshal(data, &out)
	return out, resp.StatusCode, nil
}

// ---------- WhatsApp -> Chatwoot (entrada) ----------

// realPhone devolve o telefone real (PN). Se o JID for um LID, tenta converter
// via store; senão devolve o próprio user.
func (s *Session) realPhone(jid types.JID) string {
	if jid.User == "" {
		return ""
	}
	if jid.Server == types.DefaultUserServer {
		return jid.User
	}
	if pn, err := s.client.Store.LIDs.GetPNForLID(context.Background(), jid); err == nil && pn.User != "" {
		return pn.User
	}
	return jid.User
}

func (s *Session) chatwootPushIncoming(evt *events.Message) {
	cfg := s.getChatwoot()
	if !cfg.valid() || evt.Info.IsFromMe || evt.Info.IsGroup {
		return
	}
	// telefone real (PN), nunca o LID
	chat := evt.Info.Chat
	phone := chat.User
	if chat.Server != types.DefaultUserServer {
		if evt.Info.SenderAlt.Server == types.DefaultUserServer && evt.Info.SenderAlt.User != "" {
			phone = evt.Info.SenderAlt.User
		} else {
			phone = s.realPhone(chat)
		}
	}
	chatID := phone + "@" + types.DefaultUserServer
	if isGroupChatID(chat.String()) {
		chatID = chat.String()
	}
	name := evt.Info.PushName
	if name == "" {
		name = phone
	}

	avatar := ""
	if pp, perr := s.client.GetProfilePictureInfo(context.Background(), evt.Info.Chat, nil); perr == nil && pp != nil {
		avatar = pp.URL
	}
	contactID, sourceID, err := cfg.ensureContact(chatID, phone, name, avatar)
	if err != nil {
		s.log.Error("chatwoot: ensure contact failed", "err", err)
		return
	}
	convID, err := cfg.ensureConversation(contactID, sourceID)
	if err != nil {
		s.log.Error("chatwoot: ensure conversation failed", "err", err)
		return
	}

	text := messageText(evt.Message)
	// mídia recebida: baixa do WhatsApp e sobe pro Chatwoot como anexo
	if dl := downloadableOf(evt.Message); dl != nil {
		data, derr := s.client.Download(context.Background(), dl)
		if derr == nil && len(data) > 0 {
			fname, mime := mediaMeta(evt.Message)
			if uerr := cfg.postAttachment(convID, text, fname, mime, data); uerr != nil {
				s.log.Error("chatwoot: post attachment failed", "err", uerr)
			} else {
				return
			}
		}
	}
	if strings.TrimSpace(text) == "" {
		return
	}
	if err := cfg.postText(convID, text); err != nil {
		s.log.Error("chatwoot: post message failed", "err", err)
	}
}

// avatarSynced evita re-sincronizar a foto a cada mensagem (1x por contato/processo).
var avatarSynced sync.Map

// ensureContact acha (por telefone) ou cria o contato e garante o source_id da inbox.
func (c ChatwootConfig) ensureContact(chatID, phone, name, avatarURL string) (contactID int, sourceID string, err error) {
	if res, code, e := c.req(http.MethodGet, "/contacts/search?q="+phone, nil); e == nil && code == 200 {
		for _, it := range asList(res["payload"]) {
			m := asMap(it)
			if id := asInt(m["id"]); id != 0 {
				ident := asStr(m["identifier"])
				attr := ""
				if ca := asMap(m["custom_attributes"]); ca != nil {
					attr = asStr(ca[cwChatIDAttr])
				}
				if isGroupChatID(ident) && ident != chatID {
					continue
				}
				if isGroupChatID(attr) && attr != chatID {
					continue
				}
				if isGroupChatID(chatID) && ident != chatID && attr != chatID {
					continue
				}
				c.syncAvatar(id, avatarURL)
				if sid := sourceIDForInbox(m, c.InboxID); sid != "" {
					return id, sid, nil
				}
				sid, e2 := c.ensureContactInbox(id)
				return id, sid, e2
			}
		}
	}
	// cria contato
	body := map[string]any{
		"inbox_id":     c.InboxID,
		"name":         name,
		"phone_number": "+" + phone,
		"identifier":   chatID,
		"custom_attributes": map[string]any{
			cwChatIDAttr: chatID,
		},
	}
	if avatarURL != "" {
		body["avatar_url"] = avatarURL
	}
	res, code, e := c.req(http.MethodPost, "/contacts", body)
	if e != nil {
		return 0, "", e
	}
	if code >= 300 {
		return 0, "", fmt.Errorf("create contact http %d", code)
	}
	contact := asMap(asMap(res["payload"])["contact"])
	id := asInt(contact["id"])
	if avatarURL != "" {
		avatarSynced.Store(fmt.Sprintf("%d:%d", c.AccountID, id), true)
	}
	sid := sourceIDForInbox(contact, c.InboxID)
	if sid == "" {
		sid, _ = c.ensureContactInbox(id)
	}
	return id, sid, nil
}

// syncAvatar atualiza a foto do contato existente (uma vez por processo).
func (c ChatwootConfig) syncAvatar(contactID int, avatarURL string) {
	if avatarURL == "" {
		return
	}
	key := fmt.Sprintf("%d:%d", c.AccountID, contactID)
	if _, done := avatarSynced.LoadOrStore(key, true); done {
		return
	}
	_, _, _ = c.req(http.MethodPut, fmt.Sprintf("/contacts/%d", contactID), map[string]any{"avatar_url": avatarURL})
}

func (c ChatwootConfig) ensureContactInbox(contactID int) (string, error) {
	body := map[string]any{"inbox_id": c.InboxID}
	res, _, e := c.req(http.MethodPost, fmt.Sprintf("/contacts/%d/contact_inboxes", contactID), body)
	if e != nil {
		return "", e
	}
	return asStr(res["source_id"]), nil
}

// ensureConversation reutiliza uma conversa aberta da inbox ou cria uma nova.
func (c ChatwootConfig) ensureConversation(contactID int, sourceID string) (int, error) {
	if res, code, e := c.req(http.MethodGet, fmt.Sprintf("/contacts/%d/conversations", contactID), nil); e == nil && code == 200 {
		for _, it := range asList(res["payload"]) {
			m := asMap(it)
			if asInt(m["inbox_id"]) == c.InboxID {
				st := asStr(m["status"])
				if st == "open" || st == "pending" || st == "snoozed" {
					return asInt(m["id"]), nil
				}
			}
		}
	}
	body := map[string]any{
		"source_id": sourceID, "inbox_id": c.InboxID, "contact_id": contactID, "status": "open",
	}
	res, code, e := c.req(http.MethodPost, "/conversations", body)
	if e != nil {
		return 0, e
	}
	if code >= 300 {
		return 0, fmt.Errorf("create conversation http %d", code)
	}
	return asInt(res["id"]), nil
}

func (c ChatwootConfig) postText(convID int, content string) error {
	_, code, e := c.req(http.MethodPost, fmt.Sprintf("/conversations/%d/messages", convID), map[string]any{
		"content": content, "message_type": "incoming", "content_type": "text",
	})
	if e != nil {
		return e
	}
	if code >= 300 {
		return fmt.Errorf("post message http %d", code)
	}
	return nil
}

// postAttachment sobe a mídia como anexo (multipart) numa mensagem incoming.
func (c ChatwootConfig) postAttachment(convID int, content, filename, mime string, data []byte) error {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("message_type", "incoming")
	if content != "" {
		_ = mw.WriteField("content", content)
	}
	h := make(map[string][]string)
	h["Content-Disposition"] = []string{fmt.Sprintf(`form-data; name="attachments[]"; filename=%q`, filename)}
	h["Content-Type"] = []string{mime}
	pw, _ := mw.CreatePart(h)
	_, _ = pw.Write(data)
	mw.Close()

	url := c.base() + fmt.Sprintf("/conversations/%d/messages", convID)
	req, err := http.NewRequest(http.MethodPost, url, &buf)
	if err != nil {
		return err
	}
	req.Header.Set("api_access_token", c.AccountToken)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := cwHTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("post attachment http %d", resp.StatusCode)
	}
	return nil
}

// ---------- Chatwoot -> WhatsApp (saída via webhook) ----------

func (s *server) handleChatwootWebhook(w http.ResponseWriter, r *http.Request) {
	sess := s.sessionByID(w, r.PathValue("sid"))
	if sess == nil {
		return
	}
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid payload"})
		return
	}
	// só processa mensagens de saída do agente
	if asStr(body["event"]) != "message_created" || asStr(body["message_type"]) != "outgoing" {
		w.WriteHeader(http.StatusOK)
		return
	}
	if b, ok := body["private"].(bool); ok && b {
		w.WriteHeader(http.StatusOK)
		return
	}

	chatID := chatIDFromWebhook(body)
	if chatID == "" {
		w.WriteHeader(http.StatusOK)
		return
	}
	jid, err := resolveRecipient(chatID)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	content := asStr(body["content"])
	attachments := asList(body["attachments"])
	ctx := r.Context()

	// texto (só envia separado se não houver exatamente 1 anexo, igual ao WAHA)
	if strings.TrimSpace(content) != "" && len(attachments) != 1 {
		_, _ = sess.client.SendMessage(ctx, jid, &waE2E.Message{Conversation: proto.String(content)})
	}
	// anexos
	for _, it := range attachments {
		a := asMap(it)
		url := asStr(a["data_url"])
		if url == "" {
			continue
		}
		caption := ""
		if len(attachments) == 1 {
			caption = content
		}
		if err := sess.sendChatwootFile(ctx, jid, asStr(a["file_type"]), url, caption); err != nil {
			s.log.Error("chatwoot->wa: send file failed", "err", err)
		}
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// sendChatwootFile baixa o anexo do Chatwoot e envia pelo WhatsApp.
func (s *Session) sendChatwootFile(ctx context.Context, jid types.JID, fileType, url, caption string) error {
	data, err := fetchMedia("", url)
	if err != nil {
		return err
	}
	filename := url[strings.LastIndex(url, "/")+1:]
	switch fileType {
	case "image":
		up, e := s.client.Upload(ctx, data, whatsmeow.MediaImage)
		if e != nil {
			return e
		}
		_, e = s.client.SendMessage(ctx, jid, &waE2E.Message{ImageMessage: &waE2E.ImageMessage{
			Caption: proto.String(caption), Mimetype: proto.String("image/jpeg"),
			URL: &up.URL, DirectPath: &up.DirectPath, MediaKey: up.MediaKey,
			FileEncSHA256: up.FileEncSHA256, FileSHA256: up.FileSHA256, FileLength: proto.Uint64(up.FileLength),
		}})
		return e
	case "audio":
		ogg, seconds, waveform, terr := transcodeVoice(data)
		if terr != nil {
			ogg = data // fallback: envia o original
		}
		up, e := s.client.Upload(ctx, ogg, whatsmeow.MediaAudio)
		if e != nil {
			return e
		}
		am := &waE2E.AudioMessage{
			Mimetype: proto.String("audio/ogg; codecs=opus"), PTT: proto.Bool(true),
			URL: &up.URL, DirectPath: &up.DirectPath, MediaKey: up.MediaKey,
			FileEncSHA256: up.FileEncSHA256, FileSHA256: up.FileSHA256, FileLength: proto.Uint64(up.FileLength),
		}
		if terr == nil {
			am.Seconds = proto.Uint32(seconds)
			am.Waveform = waveform
		}
		_, e = s.client.SendMessage(ctx, jid, &waE2E.Message{AudioMessage: am})
		return e
	case "video":
		up, e := s.client.Upload(ctx, data, whatsmeow.MediaVideo)
		if e != nil {
			return e
		}
		_, e = s.client.SendMessage(ctx, jid, &waE2E.Message{VideoMessage: &waE2E.VideoMessage{
			Caption: proto.String(caption), Mimetype: proto.String("video/mp4"),
			URL: &up.URL, DirectPath: &up.DirectPath, MediaKey: up.MediaKey,
			FileEncSHA256: up.FileEncSHA256, FileSHA256: up.FileSHA256, FileLength: proto.Uint64(up.FileLength),
		}})
		return e
	default:
		up, e := s.client.Upload(ctx, data, whatsmeow.MediaDocument)
		if e != nil {
			return e
		}
		_, e = s.client.SendMessage(ctx, jid, &waE2E.Message{DocumentMessage: &waE2E.DocumentMessage{
			FileName: proto.String(filename), Title: proto.String(filename),
			Mimetype: proto.String("application/octet-stream"),
			URL:      &up.URL, DirectPath: &up.DirectPath, MediaKey: up.MediaKey,
			FileEncSHA256: up.FileEncSHA256, FileSHA256: up.FileSHA256, FileLength: proto.Uint64(up.FileLength),
		}})
		return e
	}
}

// transcodeVoice converte um áudio qualquer em OGG/Opus (nota de voz) e calcula
// a duração e o waveform (64 bytes) p/ o WhatsApp mostrar as ondinhas e o tempo.
func transcodeVoice(input []byte) (ogg []byte, seconds uint32, waveform []byte, err error) {
	tmp, err := os.CreateTemp("", "cwaud-*")
	if err != nil {
		return nil, 0, nil, err
	}
	defer os.Remove(tmp.Name())
	if _, err = tmp.Write(input); err != nil {
		tmp.Close()
		return nil, 0, nil, err
	}
	tmp.Close()

	var oggBuf bytes.Buffer
	c1 := exec.Command("ffmpeg", "-y", "-i", tmp.Name(), "-ac", "1", "-ar", "48000", "-c:a", "libopus", "-b:a", "32k", "-f", "ogg", "pipe:1")
	c1.Stdout = &oggBuf
	if err = c1.Run(); err != nil {
		return nil, 0, nil, err
	}

	var pcmBuf bytes.Buffer
	c2 := exec.Command("ffmpeg", "-y", "-i", tmp.Name(), "-ac", "1", "-ar", "8000", "-f", "s16le", "pipe:1")
	c2.Stdout = &pcmBuf
	if err = c2.Run(); err != nil {
		return oggBuf.Bytes(), 0, nil, err
	}
	pcm := pcmBuf.Bytes()
	seconds = uint32(len(pcm) / 2 / 8000)
	return oggBuf.Bytes(), seconds, computeWaveform(pcm), nil
}

func computeWaveform(pcm []byte) []byte {
	const buckets = 64
	out := make([]byte, buckets)
	n := len(pcm) / 2
	if n == 0 {
		return out
	}
	per := n / buckets
	if per < 1 {
		per = 1
	}
	rms := make([]float64, buckets)
	var maxv float64
	for b := 0; b < buckets; b++ {
		start := b * per
		if start >= n {
			break
		}
		end := start + per
		if end > n {
			end = n
		}
		var sum float64
		for i := start; i < end; i++ {
			s := int16(binary.LittleEndian.Uint16(pcm[i*2:]))
			v := float64(s) / 32768.0
			sum += v * v
		}
		r := math.Sqrt(sum / float64(end-start))
		rms[b] = r
		if r > maxv {
			maxv = r
		}
	}
	if maxv > 0 {
		for b := 0; b < buckets; b++ {
			out[b] = byte(rms[b] / maxv * 100)
		}
	}
	return out
}

// extrai o chat id do WhatsApp a partir do payload do webhook do Chatwoot
func chatIDFromWebhook(body map[string]any) string {
	sender := asMap(asMap(asMap(body["conversation"])["meta"])["sender"])
	if ca := asMap(sender["custom_attributes"]); ca != nil {
		if v := asStr(ca[cwChatIDAttr]); v != "" {
			return v
		}
	}
	if id := asStr(sender["identifier"]); id != "" {
		return id
	}
	if ph := asStr(sender["phone_number"]); ph != "" {
		return strings.TrimPrefix(ph, "+")
	}
	return ""
}

// handleChatwootResolve: dado account_id + conversation_id, descobre a sessão
// ligada e o telefone do contato (consultando a API do Chatwoot). Usado pelo widget.
func (s *server) handleChatwootResolve(w http.ResponseWriter, r *http.Request) {
	accountID := asInt(r.URL.Query().Get("account_id"))
	convID := r.URL.Query().Get("conversation_id")
	if accountID == 0 || convID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "account_id and conversation_id required"})
		return
	}
	s.log.Info("chatwoot resolve", "account_id", accountID, "conversation_id", convID)
	// Qualquer sessão da conta serve só para consultar a conversa (mesmo token de conta).
	probe := s.sessions.sessionForChatwootAccount(accountID)
	if probe == nil {
		s.log.Warn("chatwoot resolve: no session for account", "account_id", accountID)
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no session linked to this chatwoot account"})
		return
	}
	res, code, err := probe.getChatwoot().req(http.MethodGet, "/conversations/"+convID, nil)
	if err != nil || code >= 300 {
		s.log.Error("chatwoot resolve: lookup failed", "code", code, "err", err)
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "chatwoot lookup failed"})
		return
	}
	// Amarra empresa + caixa: a sessão tem que ser a da inbox desta conversa.
	inboxID := asInt(res["inbox_id"])
	sess := s.sessions.sessionForChatwootInbox(accountID, inboxID)
	if sess == nil {
		s.log.Warn("chatwoot resolve: no session for inbox", "account_id", accountID, "inbox_id", inboxID)
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no session linked to this inbox", "inbox_id": strconv.Itoa(inboxID)})
		return
	}
	sender := asMap(asMap(res["meta"])["sender"])
	name := asStr(sender["name"])
	phone := ""
	if ca := asMap(sender["custom_attributes"]); ca != nil {
		raw := asStr(ca[cwChatIDAttr])
		if raw != "" && !isGroupChatID(raw) {
			if jid, e := types.ParseJID(raw); e == nil {
				phone = sess.realPhone(jid)
			}
		}
	}
	if phone == "" {
		phone = digitsOnly(asStr(sender["phone_number"]))
	}
	if phone == "" {
		s.log.Warn("chatwoot resolve: contact has no phone", "conversation_id", convID)
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "contact has no phone"})
		return
	}
	s.log.Info("chatwoot resolve ok", "session", sess.id, "inbox_id", inboxID, "phone", phone, "name", name)
	writeJSON(w, http.StatusOK, map[string]any{"session_id": sess.id, "inbox_id": inboxID, "phone": phone, "name": name})
}

func digitsOnly(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// ---------- handlers de config ----------

func (s *server) handleSetChatwoot(w http.ResponseWriter, r *http.Request) {
	sess := s.sessionByID(w, r.PathValue("sid"))
	if sess == nil {
		return
	}
	var cfg ChatwootConfig
	if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid payload"})
		return
	}
	// se o token vier vazio (edição), mantém o atual
	if cfg.AccountToken == "" {
		cfg.AccountToken = sess.getChatwoot().AccountToken
	}
	if !cfg.valid() {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "url, account_id, account_token e inbox_id são obrigatórios"})
		return
	}
	sess.setChatwoot(cfg)
	b, _ := json.Marshal(cfg)
	_ = sess.mgr.store.setChatwoot(r.Context(), sess.id, string(b))
	writeJSON(w, http.StatusOK, map[string]any{"chatwoot": cfg})
}

func (s *server) handleGetChatwoot(w http.ResponseWriter, r *http.Request) {
	sess := s.sessionByID(w, r.PathValue("sid"))
	if sess == nil {
		return
	}
	cfg := sess.getChatwoot()
	cfg.AccountToken = "" // não devolve o token
	writeJSON(w, http.StatusOK, map[string]any{"chatwoot": cfg, "enabled": sess.getChatwoot().valid()})
}

func (s *server) handleDeleteChatwoot(w http.ResponseWriter, r *http.Request) {
	sess := s.sessionByID(w, r.PathValue("sid"))
	if sess == nil {
		return
	}
	sess.setChatwoot(ChatwootConfig{})
	_ = sess.mgr.store.setChatwoot(r.Context(), sess.id, "")
	w.WriteHeader(http.StatusNoContent)
}

// ---------- helpers de JSON dinâmico ----------

func asMap(v any) map[string]any { m, _ := v.(map[string]any); return m }
func asList(v any) []any         { l, _ := v.([]any); return l }
func asStr(v any) string         { s, _ := v.(string); return s }
func asInt(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case string:
		i, _ := strconv.Atoi(n)
		return i
	}
	return 0
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// downloadableOf devolve a parte de mídia da mensagem (ou nil se for texto).
func downloadableOf(m *waE2E.Message) whatsmeow.DownloadableMessage {
	switch {
	case m.GetImageMessage() != nil:
		return m.GetImageMessage()
	case m.GetAudioMessage() != nil:
		return m.GetAudioMessage()
	case m.GetVideoMessage() != nil:
		return m.GetVideoMessage()
	case m.GetDocumentMessage() != nil:
		return m.GetDocumentMessage()
	}
	return nil
}

// mediaMeta devolve (filename, mimetype) p/ a mídia recebida.
func mediaMeta(m *waE2E.Message) (string, string) {
	switch {
	case m.GetImageMessage() != nil:
		return "image.jpg", firstNonEmpty(m.GetImageMessage().GetMimetype(), "image/jpeg")
	case m.GetAudioMessage() != nil:
		return "audio.ogg", firstNonEmpty(m.GetAudioMessage().GetMimetype(), "audio/ogg")
	case m.GetVideoMessage() != nil:
		return "video.mp4", firstNonEmpty(m.GetVideoMessage().GetMimetype(), "video/mp4")
	case m.GetDocumentMessage() != nil:
		d := m.GetDocumentMessage()
		return firstNonEmpty(d.GetFileName(), "file"), firstNonEmpty(d.GetMimetype(), "application/octet-stream")
	}
	return "file", "application/octet-stream"
}

func sourceIDForInbox(contact map[string]any, inboxID int) string {
	for _, ci := range asList(contact["contact_inboxes"]) {
		m := asMap(ci)
		if asInt(asMap(m["inbox"])["id"]) == inboxID {
			return asStr(m["source_id"])
		}
	}
	return ""
}

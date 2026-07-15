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
	"net/url"
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
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// Integração com Chatwoot (canal API), inspirada no app chatwoot do WAHA.
// Mapeia o contato Chatwoot <-> chat do WhatsApp via custom attribute.

const cwChatIDAttr = "wacalls_chat_id"

// isGroupChatID diz se um identifier/chatID é de um grupo (@g.us). Usado para não
// reutilizar contatos de grupo legado em conversas 1:1 (fix @diegotiemann, PR #11).
func isGroupChatID(id string) bool {
	return strings.HasSuffix(id, "@g.us")
}

type ChatwootConfig struct {
	URL             string `json:"url"`
	AccountID       int    `json:"account_id"`
	AccountToken    string `json:"account_token"`
	InboxID         int    `json:"inbox_id"`
	InboxIdentifier string `json:"inbox_identifier"`
	// Groups: quando true, mensagens de GRUPO também abrem/atualizam uma conversa
	// no Chatwoot (o "contato" é o próprio grupo; cada mensagem é prefixada com o
	// autor). Channels: idem para CANAIS (newsletters).
	Groups   bool `json:"groups"`
	Channels bool `json:"channels"`
	// GroupsSkipIncoming: quando true (com Groups ligado), o AstraCalls NÃO reflete as
	// mensagens dos OUTROS membros do grupo — só posta as peças que costumam faltar:
	// as mensagens que a conta manda pelo aparelho (nota privada) e os avisos de
	// entrada/saída/admin. Útil quando outra fonte já traz as mensagens do grupo pro
	// mesmo inbox (evita duplicação).
	GroupsSkipIncoming bool `json:"groups_skip_incoming"`
	// SignMsg: quando true, toda mensagem de SAÍDA (agente → cliente) sai no WhatsApp
	// com o nome do atendente prefixado (*Nome*\n...). O nome NÃO fica salvo na conversa
	// do Chatwoot, é adicionado só na hora de enviar. Paridade com o signMsg da Evolution.
	SignMsg bool `json:"sign_msg"`
	// AlwaysOnline: mantém a presença da conta sempre como "online" (envia presença
	// disponível a cada (re)conexão). ReadMessages: confirma leitura automática das
	// mensagens recebidas (envia recibo de leitura ao receber).
	AlwaysOnline bool `json:"always_online"`
	ReadMessages bool `json:"read_messages"`
	// MirrorAPI: quando true, as mensagens enviadas pela API do AstraCalls (ex.: n8n)
	// também aparecem no Chatwoot como NOTA PRIVADA — mesmo tratamento que as enviadas
	// pelo aparelho. Assim o atendente vê o que foi disparado por fora. As mensagens
	// do agente do Chatwoot nunca entram aqui (já estão na conversa).
	MirrorAPI bool `json:"mirror_api"`
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

// shouldMirrorOwn decide se uma mensagem from_me deve ser espelhada no Chatwoot
// como nota privada. Espelha o que a conta enviou por fora do Chatwoot: sempre o
// que sai do APARELHO, e o que sai da nossa API quando mirror_api está ligado. O
// que o agente mandou pelo próprio Chatwoot nunca é espelhado — já está na
// conversa, espelhar duplicaria.
func (s *Session) shouldMirrorOwn(msgID string, cfg ChatwootConfig) bool {
	origin, sent := s.selfSentOrigin(msgID)
	if !sent {
		return true // veio do aparelho
	}
	return origin == selfSentAPI && cfg.MirrorAPI
}

func (s *Session) chatwootPushIncoming(evt *events.Message) {
	cfg := s.getChatwoot()
	if !cfg.valid() {
		return
	}
	if evt.Info.IsFromMe {
		if !s.shouldMirrorOwn(evt.Info.ID, cfg) {
			return
		}
		switch evt.Info.Chat.Server {
		case types.DefaultUserServer, types.HiddenUserServer:
			s.chatwootMirrorOwn(cfg, evt)
		case types.GroupServer:
			if cfg.Groups {
				s.chatwootMirrorOwnGroup(cfg, evt)
			}
		}
		return
	}
	switch evt.Info.Chat.Server {
	case types.GroupServer:
		if cfg.Groups && !cfg.GroupsSkipIncoming {
			s.chatwootPushGroup(cfg, evt)
		}
	case types.NewsletterServer:
		if cfg.Channels {
			s.chatwootPushChannel(cfg, evt)
		}
	case types.DefaultUserServer, types.HiddenUserServer:
		s.chatwootPushDirect(cfg, evt)
	}
}

// chatwootMirrorOwn espelha, como NOTA PRIVADA, uma mensagem 1:1 que a conta
// enviou pelo aparelho — para o agente ver no Chatwoot o que foi dito por fora.
// Não é reenviado ao contato (nota privada não dispara o webhook de saída).
func (s *Session) chatwootMirrorOwn(cfg ChatwootConfig, evt *events.Message) {
	chat := evt.Info.Chat // numa msg from_me 1:1, o Chat é o destinatário
	phone := chat.User
	if chat.Server != types.DefaultUserServer {
		if evt.Info.RecipientAlt.Server == types.DefaultUserServer && evt.Info.RecipientAlt.User != "" {
			phone = evt.Info.RecipientAlt.User
		} else {
			phone = s.realPhone(chat)
		}
	}
	chatID := phone + "@" + types.DefaultUserServer
	avatar := ""
	if pp, perr := s.client.GetProfilePictureInfo(context.Background(), chat, nil); perr == nil && pp != nil {
		avatar = pp.URL
	}
	contactID, sourceID, err := cfg.ensureContact(chatID, phone, phone, avatar)
	if err != nil {
		s.log.Error("chatwoot: ensure contact (espelho) failed", "err", err)
		return
	}
	convID, err := cfg.ensureConversation(contactID, sourceID)
	if err != nil {
		s.log.Error("chatwoot: ensure conversation (espelho) failed", "err", err)
		return
	}
	s.chatwootDeliver(cfg, convID, evt, "", true)
}

// chatwootPushDirect trata a conversa 1:1 (comportamento original).
func (s *Session) chatwootPushDirect(cfg ChatwootConfig, evt *events.Message) {
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
	s.chatwootDeliver(cfg, convID, evt, "", false)
}

// chatwootPushGroup abre/atualiza uma conversa no Chatwoot para um GRUPO. O
// "contato" é o próprio grupo (identificado pelo JID @g.us) e cada mensagem é
// prefixada com o nome/telefone de quem escreveu, já que a inbox tem 1 contato
// por conversa.
func (s *Session) chatwootPushGroup(cfg ChatwootConfig, evt *events.Message) {
	convID, err := s.groupConversation(cfg, evt.Info.Chat)
	if err != nil {
		s.log.Error("chatwoot: ensure group conversation failed", "err", err)
		return
	}
	author := evt.Info.PushName
	if author == "" {
		author = s.realPhone(evt.Info.Sender)
	}
	s.chatwootDeliver(cfg, convID, evt, "*"+author+"*:\n", false)
}

// chatwootMirrorOwnGroup espelha, como NOTA PRIVADA na conversa do grupo, uma
// mensagem que a conta enviou PELO APARELHO dentro de um grupo — para o agente ver
// no Chatwoot o que o dono da conta falou por fora. Não reenvia nada ao grupo.
func (s *Session) chatwootMirrorOwnGroup(cfg ChatwootConfig, evt *events.Message) {
	convID, err := s.groupConversation(cfg, evt.Info.Chat)
	if err != nil {
		s.log.Error("chatwoot: ensure group conversation (espelho) failed", "err", err)
		return
	}
	author := s.client.Store.PushName
	if author == "" {
		author = "Você"
	}
	s.chatwootDeliver(cfg, convID, evt, "*"+author+" (você)*:\n", true)
}

// groupConversation acha/cria o contato "grupo" (JID @g.us) e sua conversa.
func (s *Session) groupConversation(cfg ChatwootConfig, group types.JID) (int, error) {
	chatID := group.String() // 1203...@g.us
	name := chatID
	if gi, err := s.client.GetGroupInfo(context.Background(), group); err == nil && gi.Name != "" {
		name = gi.Name
	}
	avatar := ""
	if pp, perr := s.client.GetProfilePictureInfo(context.Background(), group, nil); perr == nil && pp != nil {
		avatar = pp.URL
	}
	contactID, sourceID, err := cfg.ensureContact(chatID, "", name, avatar)
	if err != nil {
		return 0, err
	}
	return cfg.ensureConversation(contactID, sourceID)
}

// chatwootPushChannel abre/atualiza uma conversa no Chatwoot para um CANAL
// (newsletter). O contato é o canal; as mensagens vêm do próprio canal, então
// não há prefixo de autor.
func (s *Session) chatwootPushChannel(cfg ChatwootConfig, evt *events.Message) {
	channel := evt.Info.Chat
	chatID := channel.String() // ...@newsletter
	name := chatID
	if ni, err := s.client.GetNewsletterInfo(context.Background(), channel); err == nil && ni.ThreadMeta.Name.Text != "" {
		name = ni.ThreadMeta.Name.Text
	}
	contactID, sourceID, err := cfg.ensureContact(chatID, "", "📢 "+name, "")
	if err != nil {
		s.log.Error("chatwoot: ensure channel contact failed", "err", err)
		return
	}
	convID, err := cfg.ensureConversation(contactID, sourceID)
	if err != nil {
		s.log.Error("chatwoot: ensure channel conversation failed", "err", err)
		return
	}
	s.chatwootDeliver(cfg, convID, evt, "", false)
}

// chatwootDeliver baixa a mídia (se houver) e posta a mensagem na conversa.
// prefix é acrescentado ao texto (usado em grupos p/ identificar o autor).
func (s *Session) chatwootDeliver(cfg ChatwootConfig, convID int, evt *events.Message, prefix string, private bool) {
	text := messageText(evt.Message)
	// visualização única: sinaliza pro atendente (a mídia baixa e sobe normal)
	if _, viewOnce := unwrapViewOnce(evt.Message); viewOnce {
		text = strings.TrimRight("👁️ _Visualização única_\n"+text, "\n")
	}
	// enquete: anexa o ID da mensagem (p/ referenciar no endpoint de voto)
	if getPoll(evt.Message) != nil && evt.Info.ID != "" {
		text += "\n_PID: " + evt.Info.ID + "_"
	}
	// evento: anexa o ID da mensagem (p/ referenciar no endpoint de RSVP)
	if evt.Message.GetEventMessage() != nil && evt.Info.ID != "" {
		text += "\n_EID: " + evt.Info.ID + "_"
	}
	// resposta com citação: source_id = ID da msg do WhatsApp; in_reply_to = a msg citada
	sourceID := evt.Info.ID
	inReplyTo := ""
	if ci := messageContextInfo(evt.Message); ci != nil {
		inReplyTo = ci.GetStanzaID()
	}
	// mídia: baixa do WhatsApp e sobe pro Chatwoot como anexo
	if dl := downloadableOf(evt.Message); dl != nil {
		data, derr := s.client.Download(context.Background(), dl)
		if derr == nil && len(data) > 0 {
			fname, mime := mediaMeta(evt.Message)
			if uerr := cfg.postAttachment(convID, prefix+text, fname, mime, data, private, sourceID, inReplyTo); uerr != nil {
				s.log.Error("chatwoot: post attachment failed", "err", uerr)
			}
			return
		}
	}
	if strings.TrimSpace(text) == "" {
		return
	}
	if err := cfg.postText(convID, prefix+text, private, sourceID, inReplyTo); err != nil {
		s.log.Error("chatwoot: post message failed", "err", err)
	}
}

// avatarSynced evita re-sincronizar a foto a cada mensagem (1x por contato/processo).
var avatarSynced sync.Map

// ensureContact acha (por telefone, ou por identifier quando phone == "" no caso
// de grupos/canais) ou cria o contato e garante o source_id da inbox.
func (c ChatwootConfig) ensureContact(chatID, phone, name, avatarURL string) (contactID int, sourceID string, err error) {
	// grupos/canais não têm telefone -> busca pelo identifier (o JID)
	query := phone
	if query == "" {
		query = chatID
	}
	if res, code, e := c.req(http.MethodGet, "/contacts/search?q="+url.QueryEscape(query), nil); e == nil && code == 200 {
		for _, it := range asList(res["payload"]) {
			m := asMap(it)
			ident := asStr(m["identifier"])
			attr := ""
			if ca := asMap(m["custom_attributes"]); ca != nil {
				attr = asStr(ca[cwChatIDAttr])
			}
			// Fix (@diegotiemann, PR #11): numa busca 1:1 por telefone, não
			// reutilizar um contato de GRUPO legado ({phone}-{ts}@g.us) que casou
			// pelo número.
			if isGroupChatID(ident) && ident != chatID {
				continue
			}
			if isGroupChatID(attr) && attr != chatID {
				continue
			}
			// grupos/canais (busca por identifier): exige match exato do JID/attr.
			if phone == "" && ident != chatID && attr != chatID {
				continue
			}
			if id := asInt(m["id"]); id != 0 {
				c.syncAvatar(id, avatarURL)
				if sid := sourceIDForInbox(m, c.InboxID); sid != "" {
					return id, sid, nil
				}
				// achou contato mas sem source_id p/ esta inbox -> cria contact_inbox
				sid, e2 := c.ensureContactInbox(id)
				return id, sid, e2
			}
		}
	}
	// cria contato
	body := map[string]any{
		"inbox_id":   c.InboxID,
		"name":       name,
		"identifier": chatID,
		"custom_attributes": map[string]any{
			cwChatIDAttr: chatID,
		},
	}
	if phone != "" {
		body["phone_number"] = "+" + phone
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

func (c ChatwootConfig) postText(convID int, content string, private bool, sourceID, inReplyTo string) error {
	body := map[string]any{"content": content, "message_type": "incoming", "content_type": "text"}
	if private {
		// nota privada: registro interno p/ o agente, não reenvia ao contato
		body["message_type"] = "outgoing"
		body["private"] = true
	}
	if sourceID != "" {
		body["source_id"] = sourceID // = ID da msg do WhatsApp (elo p/ resposta)
	}
	if inReplyTo != "" {
		body["content_attributes"] = map[string]any{"in_reply_to_external_id": inReplyTo}
	}
	_, code, e := c.req(http.MethodPost, fmt.Sprintf("/conversations/%d/messages", convID), body)
	if e != nil {
		return e
	}
	if code >= 300 {
		return fmt.Errorf("post message http %d", code)
	}
	return nil
}

// postAttachment sobe a mídia como anexo (multipart) numa mensagem incoming.
func (c ChatwootConfig) postAttachment(convID int, content, filename, mime string, data []byte, private bool, sourceID, inReplyTo string) error {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	if private {
		_ = mw.WriteField("message_type", "outgoing")
		_ = mw.WriteField("private", "true")
	} else {
		_ = mw.WriteField("message_type", "incoming")
	}
	if content != "" {
		_ = mw.WriteField("content", content)
	}
	if sourceID != "" {
		_ = mw.WriteField("source_id", sourceID)
	}
	if inReplyTo != "" {
		ca, _ := json.Marshal(map[string]string{"in_reply_to_external_id": inReplyTo})
		_ = mw.WriteField("content_attributes", string(ca))
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

	// assinatura do atendente: prefixa *Nome*\n no texto e na legenda de mídia.
	// O nome vem do sender do webhook (o agente). Ignora quando não há nome
	// (ex.: automações/bot) pra não sair "undefined".
	sign := func(text string) string { return text }
	if sess.getChatwoot().SignMsg {
		if name := chatwootSenderName(body); name != "" {
			sign = func(text string) string {
				if strings.TrimSpace(text) == "" {
					return text
				}
				return "*" + name + "*\n" + text
			}
		}
	}

	// se o agente respondeu uma mensagem, monta o contexto de citação
	quote := sess.quoteContext(ctx, body)

	var waMsgID string // ID da 1ª msg do WhatsApp enviada (vira source_id no Chatwoot)

	// texto (só envia separado se não houver exatamente 1 anexo)
	if strings.TrimSpace(content) != "" && len(attachments) != 1 {
		signed := sign(content)
		var msg *waE2E.Message
		if quote != nil {
			msg = &waE2E.Message{ExtendedTextMessage: &waE2E.ExtendedTextMessage{
				Text: proto.String(signed), ContextInfo: quote,
			}}
		} else {
			msg = &waE2E.Message{Conversation: proto.String(signed)}
		}
		if id, e := sess.sendAndMark(ctx, jid, msg); e == nil {
			waMsgID = id
		}
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
			caption = sign(content)
		}
		id, ferr := sess.sendChatwootFile(ctx, jid, asStr(a["file_type"]), url, caption, quote)
		if ferr != nil {
			s.log.Error("chatwoot->wa: send file failed", "err", ferr)
		} else if waMsgID == "" {
			waMsgID = id
		}
	}
	// grava o source_id na mensagem de SAÍDA do Chatwoot (amarra citação cliente->agente)
	if waMsgID != "" {
		if cwMsgID := asInt(body["id"]); cwMsgID != 0 {
			go sess.setMessageSourceID(cwMsgID, waMsgID)
		}
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// chatwootSenderName extrai o nome do atendente do webhook de saída. Retorna ""
// quando não há remetente humano (automações/bot sem nome), pra não assinar.
func chatwootSenderName(body map[string]any) string {
	sender := asMap(body["sender"])
	if sender == nil {
		return ""
	}
	// só assina quando o remetente é um usuário/agente (não contato/bot sem nome)
	if t := asStr(sender["type"]); t != "" && t != "user" && t != "agent" {
		return ""
	}
	if n := strings.TrimSpace(asStr(sender["name"])); n != "" {
		return n
	}
	return strings.TrimSpace(asStr(sender["available_name"]))
}

// quoteContext monta o ContextInfo de citação a partir do webhook do Chatwoot.
// Usa in_reply_to_external_id (o ID da msg do WhatsApp que setamos como source_id);
// se vier só in_reply_to (id da msg no Chatwoot), resolve o source_id via API.
func (s *Session) quoteContext(ctx context.Context, body map[string]any) *waE2E.ContextInfo {
	ca := asMap(body["content_attributes"])
	if ca == nil {
		return nil
	}
	extID := asStr(ca["in_reply_to_external_id"])
	if extID == "" {
		if rid := asInt(ca["in_reply_to"]); rid != 0 {
			convID := asInt(asMap(body["conversation"])["id"])
			extID = s.getChatwoot().messageSourceID(convID, rid)
		}
	}
	if extID == "" {
		return nil
	}
	_, senderStr, _, raw, err := s.mgr.store.findMessage(ctx, s.id, extID)
	if err != nil {
		return nil
	}
	ci := &waE2E.ContextInfo{StanzaID: proto.String(extID)}
	if senderStr != "" {
		ci.Participant = proto.String(senderStr)
	}
	if len(raw) > 0 {
		var qm waE2E.Message
		if protojson.Unmarshal(raw, &qm) == nil {
			ci.QuotedMessage = &qm
		}
	}
	return ci
}

// setMessageSourceID grava o ID da msg do WhatsApp como source_id da mensagem de
// SAÍDA no Chatwoot (endpoint custom do dev), p/ amarrar a citação quando o
// cliente responde uma mensagem do agente. Fire-and-forget.
func (s *Session) setMessageSourceID(chatwootMsgID int, sourceID string) {
	cfg := s.getChatwoot()
	if !cfg.valid() {
		return
	}
	_, code, err := cfg.req(http.MethodPost, "/kanban/connections/set_message_source_id", map[string]any{
		"message_id": chatwootMsgID,
		"source_id":  sourceID,
	})
	if err != nil {
		s.log.Warn("chatwoot: set_message_source_id falhou", "err", err)
	} else if code >= 300 {
		s.log.Warn("chatwoot: set_message_source_id http", "code", code)
	}
}

// messageSourceID busca o source_id (ID externo) de uma mensagem do Chatwoot.
func (c ChatwootConfig) messageSourceID(convID, msgID int) string {
	res, code, e := c.req(http.MethodGet, fmt.Sprintf("/conversations/%d/messages", convID), nil)
	if e != nil || code != 200 {
		return ""
	}
	for _, it := range asList(res["payload"]) {
		m := asMap(it)
		if asInt(m["id"]) == msgID {
			return asStr(m["source_id"])
		}
	}
	return ""
}

// sendChatwootFile baixa o anexo do Chatwoot e envia pelo WhatsApp (com citação opcional).
func (s *Session) sendChatwootFile(ctx context.Context, jid types.JID, fileType, url, caption string, quote *waE2E.ContextInfo) (string, error) {
	data, err := fetchMedia("", url)
	if err != nil {
		return "", err
	}
	filename := url[strings.LastIndex(url, "/")+1:]
	switch fileType {
	case "image":
		up, e := s.client.Upload(ctx, data, whatsmeow.MediaImage)
		if e != nil {
			return "", e
		}
		return s.sendAndMark(ctx, jid, &waE2E.Message{ImageMessage: &waE2E.ImageMessage{
			Caption: proto.String(caption), Mimetype: proto.String("image/jpeg"),
			URL: &up.URL, DirectPath: &up.DirectPath, MediaKey: up.MediaKey,
			FileEncSHA256: up.FileEncSHA256, FileSHA256: up.FileSHA256, FileLength: proto.Uint64(up.FileLength),
			ContextInfo: quote,
		}})
	case "audio":
		ogg, seconds, waveform, terr := transcodeVoice(data)
		if terr != nil {
			ogg = data // fallback: envia o original
		}
		up, e := s.client.Upload(ctx, ogg, whatsmeow.MediaAudio)
		if e != nil {
			return "", e
		}
		am := &waE2E.AudioMessage{
			Mimetype: proto.String("audio/ogg; codecs=opus"), PTT: proto.Bool(true),
			URL: &up.URL, DirectPath: &up.DirectPath, MediaKey: up.MediaKey,
			FileEncSHA256: up.FileEncSHA256, FileSHA256: up.FileSHA256, FileLength: proto.Uint64(up.FileLength),
			ContextInfo: quote,
		}
		if terr == nil {
			am.Seconds = proto.Uint32(seconds)
			am.Waveform = waveform
		}
		return s.sendAndMark(ctx, jid, &waE2E.Message{AudioMessage: am})
	case "video":
		up, e := s.client.Upload(ctx, data, whatsmeow.MediaVideo)
		if e != nil {
			return "", e
		}
		return s.sendAndMark(ctx, jid, &waE2E.Message{VideoMessage: &waE2E.VideoMessage{
			Caption: proto.String(caption), Mimetype: proto.String("video/mp4"),
			URL: &up.URL, DirectPath: &up.DirectPath, MediaKey: up.MediaKey,
			FileEncSHA256: up.FileEncSHA256, FileSHA256: up.FileSHA256, FileLength: proto.Uint64(up.FileLength),
			ContextInfo: quote,
		}})
	default:
		up, e := s.client.Upload(ctx, data, whatsmeow.MediaDocument)
		if e != nil {
			return "", e
		}
		return s.sendAndMark(ctx, jid, &waE2E.Message{DocumentMessage: &waE2E.DocumentMessage{
			FileName: proto.String(filename), Title: proto.String(filename),
			Mimetype: proto.String("application/octet-stream"),
			URL:      &up.URL, DirectPath: &up.DirectPath, MediaKey: up.MediaKey,
			FileEncSHA256: up.FileEncSHA256, FileSHA256: up.FileSHA256, FileLength: proto.Uint64(up.FileLength),
			ContextInfo: quote,
		}})
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
	// Fix (@diegotiemann, PR #11): prioriza o identifier (que guarda o JID de
	// grupo/canal) sobre o phone_number.
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
		// Fix (@diegotiemann, PR #11): grupo não tem telefone p/ o widget de chamada.
		if raw != "" && !isGroupChatID(raw) {
			if jid, e := types.ParseJID(raw); e == nil {
				phone = sess.realPhone(jid) // converte LID->PN se necessário
			} else {
				phone = digitsOnly(raw)
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
	// parte da config atual: o que não vier no payload é preservado (senão salvar
	// pelo painel zeraria flags como groups/sign_msg, que ele não envia).
	cfg := sess.getChatwoot()
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

// handleChatwootOpenGroup cria/garante um contato + conversa no Chatwoot para um
// grupo, sob demanda (sem esperar chegar mensagem). Requer Chatwoot configurado.
func (s *server) handleChatwootOpenGroup(w http.ResponseWriter, r *http.Request) {
	sess := s.sessionByID(w, r.PathValue("sid"))
	if sess == nil {
		return
	}
	cfg := sess.getChatwoot()
	if !cfg.valid() {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "chatwoot não configurado nesta sessão"})
		return
	}
	gid, err := resolveGroupJID(r.PathValue("gid"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	chatID := gid.String()
	name := chatID
	if gi, e := sess.client.GetGroupInfo(r.Context(), gid); e == nil && gi.Name != "" {
		name = gi.Name
	}
	avatar := ""
	if pp, perr := sess.client.GetProfilePictureInfo(r.Context(), gid, nil); perr == nil && pp != nil {
		avatar = pp.URL
	}
	s.openChatwootConversation(w, cfg, chatID, name, avatar)
}

// handleChatwootOpenChannel: idem para um canal (newsletter).
func (s *server) handleChatwootOpenChannel(w http.ResponseWriter, r *http.Request) {
	sess := s.sessionByID(w, r.PathValue("sid"))
	if sess == nil {
		return
	}
	cfg := sess.getChatwoot()
	if !cfg.valid() {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "chatwoot não configurado nesta sessão"})
		return
	}
	jid, err := resolveNewsletterJID(r.PathValue("id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	chatID := jid.String()
	name := chatID
	if ni, e := sess.client.GetNewsletterInfo(r.Context(), jid); e == nil && ni.ThreadMeta.Name.Text != "" {
		name = ni.ThreadMeta.Name.Text
	}
	s.openChatwootConversation(w, cfg, chatID, "📢 "+name, "")
}

// openChatwootConversation garante contato + conversa e devolve os ids.
func (s *server) openChatwootConversation(w http.ResponseWriter, cfg ChatwootConfig, chatID, name, avatar string) {
	contactID, sourceID, err := cfg.ensureContact(chatID, "", name, avatar)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	convID, err := cfg.ensureConversation(contactID, sourceID)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"contactId": contactID, "conversationId": convID, "chatId": chatID})
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
	m, _ = unwrapViewOnce(m)
	switch {
	case m.GetImageMessage() != nil:
		return m.GetImageMessage()
	case m.GetAudioMessage() != nil:
		return m.GetAudioMessage()
	case m.GetVideoMessage() != nil:
		return m.GetVideoMessage()
	case m.GetDocumentMessage() != nil:
		return m.GetDocumentMessage()
	case m.GetStickerMessage() != nil:
		return m.GetStickerMessage()
	case m.GetProductMessage() != nil:
		// a imagem do produto/catálogo vira anexo no Chatwoot
		if img := productImage(m.GetProductMessage()); img != nil {
			return img
		}
	}
	return nil
}

// mediaMeta devolve (filename, mimetype) p/ a mídia recebida.
func mediaMeta(m *waE2E.Message) (string, string) {
	m, _ = unwrapViewOnce(m)
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
	case m.GetStickerMessage() != nil:
		return "sticker.webp", firstNonEmpty(m.GetStickerMessage().GetMimetype(), "image/webp")
	case m.GetProductMessage() != nil:
		if img := productImage(m.GetProductMessage()); img != nil {
			return "produto.jpg", firstNonEmpty(img.GetMimetype(), "image/jpeg")
		}
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

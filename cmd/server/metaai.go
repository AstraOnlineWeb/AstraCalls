package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"go.mau.fi/whatsmeow"
	waBinary "go.mau.fi/whatsmeow/binary"
	"go.mau.fi/whatsmeow/proto/waAICommon"
	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// Meta AI pela conta conectada: a pergunta vai para o bot da Meta
// (867051314767696@bot) como se fosse o próprio app, e a resposta volta em
// pedaços (<bot edit="first|inner|last">): cada pedaço traz o TEXTO INTEIRO até
// ali, não um trecho — guardamos só o último. Conversa (thread) por chave: a
// mesma chave mantém o contexto por metaAIThreadIdle.
//
// ATENÇÃO: a memória da Meta AI é da CONTA, não da conversa — o que um cliente
// contou pode aparecer para outro. Não usar com dado pessoal de cliente.

const (
	metaAIThreadIdle     = 30 * time.Minute
	metaAIDefaultTimeout = 35 * time.Second
	metaAIPersona        = "867051314767696$760019659443059"
)

type metaAIChunk struct {
	text     string
	editType types.BotEditType
}

type metaAIThread struct {
	id   string
	last time.Time
}

type metaAIState struct {
	queue   sync.Mutex // uma pergunta por vez por sessão
	mu      sync.Mutex
	waiters map[string]chan metaAIChunk
	threads map[string]*metaAIThread
	reset   bool // a última pergunta ficou sem resposta: refaz a sessão Signal
}

var metaAIStates sync.Map // session id -> *metaAIState

func metaAIFor(sessionID string) *metaAIState {
	v, _ := metaAIStates.LoadOrStore(sessionID, &metaAIState{
		waiters: map[string]chan metaAIChunk{}, threads: map[string]*metaAIThread{},
	})
	return v.(*metaAIState)
}

// isMetaAIEvent: mensagem da conversa com a Meta AI (resposta do bot ou eco da
// pergunta feita pelo celular). Fica fora do pipeline normal (Chatwoot/webhook).
func isMetaAIEvent(evt *events.Message) bool {
	i := evt.Info
	return i.Chat.Server == types.BotServer || i.Sender.Server == types.BotServer ||
		i.Chat.User == types.MetaAIJID.User || i.Sender.User == types.MetaAIJID.User
}

// handleMetaAIEvent entrega o pedaço de resposta a quem perguntou (pelo id da pergunta).
func (s *Session) handleMetaAIEvent(evt *events.Message) {
	if evt.Info.IsFromMe {
		return
	}
	target := evt.Info.MsgMetaInfo.TargetID
	if target == "" {
		target = evt.Info.MsgBotInfo.EditTargetID
	}
	st := metaAIFor(s.id)
	st.mu.Lock()
	ch := st.waiters[string(target)]
	st.mu.Unlock()
	if ch == nil {
		return
	}
	select {
	case ch <- metaAIChunk{text: metaAIText(evt.Message), editType: evt.Info.MsgBotInfo.EditType}:
	default:
	}
}

// metaAIText tira o texto da resposta (rich response, edição, texto simples).
func metaAIText(m *waE2E.Message) string {
	if m == nil {
		return ""
	}
	if rr := m.GetRichResponseMessage(); rr != nil {
		var parts []string
		for _, sm := range rr.GetSubmessages() {
			if t := strings.TrimSpace(sm.GetMessageText()); t != "" {
				parts = append(parts, t)
			}
			if cm := sm.GetCodeMetadata(); cm != nil {
				var code strings.Builder
				for _, blk := range cm.GetCodeBlocks() {
					code.WriteString(blk.GetCodeContent())
				}
				if c := strings.TrimSpace(code.String()); c != "" {
					parts = append(parts, "```\n"+c+"\n```")
				}
			}
		}
		if len(parts) > 0 {
			return strings.Join(parts, "\n\n")
		}
	}
	if e := m.GetProtocolMessage().GetEditedMessage(); e != nil {
		return metaAIText(e)
	}
	if e := m.GetEditedMessage().GetMessage(); e != nil {
		return metaAIText(e)
	}
	if b := m.GetBotForwardedMessage().GetMessage(); b != nil {
		return metaAIText(b)
	}
	if t := m.GetConversation(); t != "" {
		return t
	}
	if t := m.GetExtendedTextMessage().GetText(); t != "" {
		return t
	}
	// formato novo/desconhecido: junta os campos de texto do JSON.
	raw, err := protojson.Marshal(m)
	if err != nil {
		return ""
	}
	var any interface{}
	_ = json.Unmarshal(raw, &any)
	var found []string
	var walk func(v interface{})
	walk = func(v interface{}) {
		switch x := v.(type) {
		case map[string]interface{}:
			for k, val := range x {
				if str, ok := val.(string); ok && (k == "messageText" || k == "text" || k == "conversation") && strings.TrimSpace(str) != "" {
					found = append(found, str)
				} else {
					walk(val)
				}
			}
		case []interface{}:
			for _, val := range x {
				walk(val)
			}
		}
	}
	walk(any)
	return strings.Join(found, "\n\n")
}

type metaAIAnswer struct {
	Text      string `json:"answer"`
	Complete  bool   `json:"complete"`
	Chunks    int    `json:"chunks"`
	Thread    string `json:"thread"`
	ThreadNew bool   `json:"threadNew"`
	PromptID  string `json:"promptId"`
	Ms        int64  `json:"ms"`
}

var errMetaAINoAnswer = errors.New("a Meta AI não respondeu a tempo")

// askMetaAI manda a pergunta (texto ou imagem com legenda) e espera a resposta
// completa. threadKey vazio = conversa nova a cada pergunta.
func (s *Session) askMetaAI(ctx context.Context, prompt, instructions, threadKey string, image *waE2E.ImageMessage, timeout time.Duration) (metaAIAnswer, error) {
	st := metaAIFor(s.id)
	st.queue.Lock()
	defer st.queue.Unlock()
	start := time.Now()

	bot := types.NewMetaAIJID
	st.mu.Lock()
	th := st.threads[threadKey]
	isNew := threadKey == "" || th == nil || time.Since(th.last) > metaAIThreadIdle
	if isNew {
		th = &metaAIThread{id: uuid.NewString()}
		if threadKey != "" {
			st.threads[threadKey] = th
		}
	}
	th.last = time.Now()
	reset := isNew || st.reset
	st.mu.Unlock()

	// A Meta AI às vezes perde o lado dela da sessão criptografada e para de
	// responder em silêncio; refazer a sessão no início de cada conversa resolve.
	if reset {
		_ = s.client.Store.Sessions.DeleteSession(ctx, bot.SignalAddress().String())
	}

	text := prompt
	if isNew && strings.TrimSpace(instructions) != "" {
		text = strings.TrimSpace(instructions) + "\n\n" + prompt
	}
	msg := &waE2E.Message{}
	if image != nil {
		image.Caption = proto.String(text)
		msg.ImageMessage = image
	} else {
		msg.ExtendedTextMessage = &waE2E.ExtendedTextMessage{Text: proto.String(text)}
	}
	msg.MessageContextInfo = &waE2E.MessageContextInfo{
		ThreadID: []*waE2E.ThreadID{{
			ThreadType: waE2E.ThreadID_AI_THREAD.Enum(),
			ThreadKey:  &waCommon.MessageKey{RemoteJID: proto.String(bot.String()), FromMe: proto.Bool(true), ID: proto.String(th.id)},
		}},
		BotMetadata: &waAICommon.BotMetadata{
			PersonaID: proto.String(metaAIPersona),
			BotThreadInfo: &waAICommon.AIThreadInfo{ClientInfo: &waAICommon.AIThreadInfo_AIThreadClientInfo{
				Type: waAICommon.AIThreadInfo_AIThreadClientInfo_DEFAULT.Enum(),
			}},
		},
	}

	id := s.client.GenerateMessageID()
	ch := make(chan metaAIChunk, 32)
	st.mu.Lock()
	st.waiters[string(id)] = ch // registrado ANTES do envio: o 1º pedaço pode chegar antes do ack
	st.mu.Unlock()
	defer func() {
		st.mu.Lock()
		delete(st.waiters, string(id))
		st.mu.Unlock()
	}()

	if _, err := s.client.SendMessage(ctx, bot, msg, whatsmeow.SendRequestExtra{ID: id}); err != nil {
		return metaAIAnswer{}, err
	}

	ans := metaAIAnswer{Thread: th.id, ThreadNew: isNew, PromptID: string(id)}
	if timeout <= 0 {
		timeout = metaAIDefaultTimeout
	}
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	// depois do "last" ainda podem vir 1–2 pedaços repetidos: espera um pouco.
	var settle <-chan time.Time
loop:
	for {
		select {
		case c := <-ch:
			ans.Chunks++
			if strings.TrimSpace(c.text) != "" {
				ans.Text = c.text
			}
			if c.editType == types.EditTypeLast || c.editType == "full" || c.editType == "" {
				ans.Complete = true
				settle = time.After(300 * time.Millisecond)
			}
		case <-settle:
			break loop
		case <-deadline.C:
			break loop
		case <-ctx.Done():
			break loop
		}
	}
	ans.Ms = time.Since(start).Milliseconds()
	st.mu.Lock()
	st.reset = ans.Chunks == 0
	if ans.Chunks == 0 && threadKey != "" {
		delete(st.threads, threadKey) // próxima pergunta abre conversa nova
	}
	st.mu.Unlock()
	if ans.Chunks == 0 {
		return ans, errMetaAINoAnswer
	}
	return ans, nil
}

// POST /api/sessions/{sid}/meta-ai
//
//	{prompt, imageUrl|base64?, thread?, instructions?, timeoutSeconds?,
//	 replyTo?, label?}
//
// Pergunta à Meta AI pela conta conectada e devolve {answer, complete, ...}.
// thread = chave da conversa (ex.: o número do cliente) para manter o contexto
// por 30 min. instructions vai antes da 1ª pergunta de cada conversa (a Meta AI
// não tem "system prompt"). replyTo = chat que recebe a resposta: manda
// "Pensando…" na hora e EDITA com a resposta; label=true marca a mensagem com o
// selo "IA" (só conta Business e só conversa privada — grupo recusa).
func (s *server) handleMetaAI(w http.ResponseWriter, r *http.Request) {
	sess := s.pairedSession(w, r.PathValue("sid"))
	if sess == nil {
		return
	}
	var b struct {
		Prompt         string `json:"prompt"`
		ImageURL       string `json:"imageUrl"`
		Base64         string `json:"base64"`
		Thread         string `json:"thread"`
		Instructions   string `json:"instructions"`
		TimeoutSeconds int    `json:"timeoutSeconds"`
		ReplyTo        string `json:"replyTo"`
		Label          bool   `json:"label"`
	}
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid body"})
		return
	}
	hasImage := b.ImageURL != "" || b.Base64 != ""
	if strings.TrimSpace(b.Prompt) == "" && !hasImage {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "prompt obrigatório"})
		return
	}
	if b.Prompt == "" {
		b.Prompt = "Descreva esta imagem."
	}
	var img *waE2E.ImageMessage
	if hasImage {
		up, ok := s.uploadMedia(sess, w, r, b.Base64, b.ImageURL, whatsmeow.MediaImage)
		if !ok {
			return
		}
		img = &waE2E.ImageMessage{
			URL: &up.URL, DirectPath: &up.DirectPath, MediaKey: up.MediaKey,
			FileEncSHA256: up.FileEncSHA256, FileSHA256: up.FileSHA256, FileLength: proto.Uint64(up.FileLength),
			Mimetype: proto.String("image/jpeg"),
		}
	}
	timeout := time.Duration(b.TimeoutSeconds) * time.Second
	if timeout <= 0 || timeout > 120*time.Second {
		timeout = metaAIDefaultTimeout
	}

	// resposta no chat: "Pensando…" já, editado com a resposta depois.
	var chat types.JID
	var placeholder types.MessageID
	if to := strings.TrimSpace(b.ReplyTo); to != "" {
		jid, err := resolveRecipient(to)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		chat = jid
		extra := whatsmeow.SendRequestExtra{}
		if b.Label && jid.Server != types.GroupServer {
			nodes := []waBinary.Node{{Tag: "bot", Attrs: waBinary.Attrs{"biz_bot": "1"}}}
			extra.AdditionalNodes = &nodes
		}
		pm := &waE2E.Message{Conversation: proto.String("Pensando…")}
		resp, err := sess.sendResolvingLID(r.Context(), jid, pm, extra)
		if err != nil && extra.AdditionalNodes != nil {
			// conta pessoal/destino que recusa o selo: manda sem ele.
			resp, err = sess.sendResolvingLID(r.Context(), jid, pm)
		}
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		placeholder = resp.ID
		sess.recordOutgoing(jid, resp.ID, resp.Timestamp.UnixMilli(), pm)
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout+15*time.Second)
	defer cancel()
	ans, err := sess.askMetaAI(ctx, b.Prompt, b.Instructions, b.Thread, img, timeout)

	out := map[string]any{"answer": ans.Text, "complete": ans.Complete, "chunks": ans.Chunks,
		"thread": ans.Thread, "threadNew": ans.ThreadNew, "promptId": ans.PromptID, "ms": ans.Ms}
	if placeholder != "" {
		final := ans.Text
		if final == "" {
			final = "A Meta AI não respondeu agora. Tente de novo em instantes."
		} else if !ans.Complete {
			final += "\n\n(resposta interrompida)"
		}
		edit := sess.client.BuildEdit(chat, placeholder, &waE2E.Message{Conversation: proto.String(final)})
		if _, eerr := sess.sendResolvingLID(context.Background(), chat, edit); eerr != nil {
			s.log.Warn("meta-ai: edição da resposta falhou", "err", eerr)
		}
		out["messageId"] = placeholder
	}
	if err != nil {
		out["error"] = err.Error()
		writeJSON(w, http.StatusGatewayTimeout, out)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

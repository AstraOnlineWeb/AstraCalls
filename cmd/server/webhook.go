package main

import (
	"encoding/json"
	"time"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/encoding/protojson"
)

// dispatchWebhook envia um evento para a URL de webhook da sessão (se houver),
// de forma assíncrona. Formato: {session, event, timestamp, data}.
func (s *Session) dispatchWebhook(event string, data any) {
	url := s.getWebhook()
	if url == "" {
		return
	}
	if !s.webhookWants(event) {
		return // filtrado: a sessão não assina este tipo de evento
	}
	body, err := json.Marshal(map[string]any{
		"session":   s.id,
		"event":     event,
		"timestamp": time.Now().UnixMilli(),
		"data":      data,
	})
	if err != nil {
		return
	}
	// Entrega confiável (retry/backoff + circuit breaker + DLQ) + assinatura HMAC
	// opcional (X-Webhook-Signature) quando há secret configurado na sessão.
	whDeliver.deliver(s.id, url, event, body, s.getWebhookSecret())
}

// summarizeMessage extrai os campos úteis de uma mensagem recebida e inclui o
// payload bruto (protojson) para integrações que precisem de mais detalhes.
func summarizeMessage(evt *events.Message) map[string]any {
	info := evt.Info
	out := map[string]any{
		"id":        info.ID,
		"chat":      info.Chat.String(),
		"sender":    info.Sender.String(),
		"fromMe":    info.IsFromMe,
		"pushName":  info.PushName,
		"timestamp": info.Timestamp.UnixMilli(),
		"isGroup":   info.IsGroup,
		"type":      messageType(evt.Message),
		"text":      messageText(evt.Message),
	}
	if _, editOrigID, isEdit := unwrapEdit(evt.Message); isEdit {
		out["edited"] = true
		out["editedId"] = editOrigID // ID da mensagem original que foi editada
	}
	if _, viewOnce := unwrapViewOnce(evt.Message); viewOnce {
		out["viewOnce"] = true
	}
	// Citação (reply): expõe no TOPO do payload o id da mensagem citada e quem a
	// enviou, pra integração não ter que cavar no raw.contextInfo — e funciona
	// igual em 1:1 e GRUPO (o ContextInfo vem em qualquer tipo com citação;
	// Conversation puro não tem citação). Espelha o par aceito no envio
	// (quotedMessageId + quotedParticipant).
	if ci := messageContextInfo(evt.Message); ci != nil {
		if sid := ci.GetStanzaID(); sid != "" {
			out["quotedMessageId"] = sid
			if p := ci.GetParticipant(); p != "" {
				out["quotedParticipant"] = p
			}
		}
	}
	// origem de anúncio (Click to WhatsApp): o "UTM" do WhatsApp, quando a msg
	// foi a primeira resposta a um anúncio do Facebook/Instagram.
	if ref := messageReferral(evt.Message); ref != nil {
		out["referral"] = ref
	}
	if raw, err := protojson.Marshal(evt.Message); err == nil {
		out["raw"] = json.RawMessage(raw)
	}
	return out
}

// messageReferral extrai a origem de anúncio "Click to WhatsApp" (CTWA) de uma
// mensagem — presente na PRIMEIRA resposta a um anúncio do Facebook/Instagram.
// Devolve um mapa amigável (estilo WAHA) com o click id, o link e os campos de
// campanha; nil quando a mensagem não veio de anúncio. É o "UTM" do WhatsApp:
// o ctwaClid casa o clique com a campanha no Meta.
func messageReferral(m *waE2E.Message) map[string]any {
	ci := messageContextInfo(m)
	if ci == nil {
		return nil
	}
	out := map[string]any{}
	if ad := ci.GetExternalAdReply(); ad != nil {
		putStr(out, "sourceType", ad.GetSourceType())
		putStr(out, "sourceId", ad.GetSourceID())
		putStr(out, "sourceUrl", ad.GetSourceURL())
		putStr(out, "sourceApp", ad.GetSourceApp())
		putStr(out, "ctwaClid", ad.GetCtwaClid())
		putStr(out, "ref", ad.GetRef())
		putStr(out, "title", ad.GetTitle())
		putStr(out, "body", ad.GetBody())
		putStr(out, "thumbnailUrl", ad.GetThumbnailURL())
		putStr(out, "mediaUrl", ad.GetMediaURL())
		if ad.MediaType != nil && ad.GetMediaType() != 0 {
			out["mediaType"] = ad.GetMediaType().String()
		}
	}
	// Campos de conversão a nível de ContextInfo (source/medium estilo UTM).
	putStr(out, "conversionSource", ci.GetConversionSource())
	putStr(out, "entryPointSource", ci.GetEntryPointConversionSource())
	putStr(out, "entryPointApp", ci.GetEntryPointConversionApp())
	putStr(out, "utmSource", ci.GetEntryPointConversionExternalSource())
	putStr(out, "utmMedium", ci.GetEntryPointConversionExternalMedium())
	putStr(out, "ctwaSignals", ci.GetCtwaSignals())
	if len(out) == 0 {
		return nil
	}
	return out
}

func putStr(m map[string]any, k, v string) {
	if v != "" {
		m[k] = v
	}
}

// unwrapEdit desembrulha uma mensagem EDITADA. O WhatsApp entrega a edição como
// um ProtocolMessage do tipo MESSAGE_EDIT: a mensagem nova fica em EditedMessage
// e a Key aponta para o ID da mensagem ORIGINAL (que já foi ao Chatwoot como
// source_id). Retorna a mensagem interna, o ID original e true quando é edição;
// caso contrário devolve a própria mensagem, "" e false.
func unwrapEdit(m *waE2E.Message) (*waE2E.Message, string, bool) {
	pm := m.GetProtocolMessage()
	if pm.GetType() == waE2E.ProtocolMessage_MESSAGE_EDIT && pm.GetEditedMessage() != nil {
		return pm.GetEditedMessage(), pm.GetKey().GetID(), true
	}
	return m, "", false
}

// unwrapViewOnce desembrulha mensagens de visualização única. No WhatsApp elas
// não chegam como ImageMessage/VideoMessage/AudioMessage no topo — vêm embrulhadas
// num FutureProofMessage (V2 = foto/vídeo, V2Extension = áudio/PTT, e o formato
// legado). A mídia interna baixa normalmente; o "ver uma vez" é só uma dica de
// exibição do cliente oficial, não muda a criptografia. Retorna a mensagem interna
// e true quando era view-once; caso contrário devolve a própria mensagem e false.
func unwrapViewOnce(m *waE2E.Message) (*waE2E.Message, bool) {
	switch {
	case m.GetViewOnceMessageV2().GetMessage() != nil:
		return m.GetViewOnceMessageV2().GetMessage(), true
	case m.GetViewOnceMessageV2Extension().GetMessage() != nil:
		return m.GetViewOnceMessageV2Extension().GetMessage(), true
	case m.GetViewOnceMessage().GetMessage() != nil:
		return m.GetViewOnceMessage().GetMessage(), true
	}
	return m, false
}

// unwrapDocCaption desembrulha o documentWithCaptionMessage (wrapper que o
// WhatsApp usa p/ documento COM legenda), devolvendo o documentMessage interno
// (que carrega o Caption). O whatsmeow já faz isso nos eventos ao vivo
// (evt.UnwrapRaw), mas o HistorySync entrega a mensagem crua — então garantimos
// aqui para não perder o arquivo/legenda na importação.
func unwrapDocCaption(m *waE2E.Message) *waE2E.Message {
	if inner := m.GetDocumentWithCaptionMessage().GetMessage(); inner != nil {
		return inner
	}
	return m
}

// messageContextInfo devolve o ContextInfo da mensagem (onde fica o StanzaID da
// mensagem citada, quando é uma resposta). Nil se não houver.
func messageContextInfo(m *waE2E.Message) *waE2E.ContextInfo {
	m, _, _ = unwrapEdit(m)
	m, _ = unwrapViewOnce(m)
	m = unwrapDocCaption(m)
	switch {
	case m.GetExtendedTextMessage() != nil:
		return m.GetExtendedTextMessage().GetContextInfo()
	case m.GetImageMessage() != nil:
		return m.GetImageMessage().GetContextInfo()
	case m.GetVideoMessage() != nil:
		return m.GetVideoMessage().GetContextInfo()
	case m.GetAudioMessage() != nil:
		return m.GetAudioMessage().GetContextInfo()
	case m.GetDocumentMessage() != nil:
		return m.GetDocumentMessage().GetContextInfo()
	case m.GetStickerMessage() != nil:
		return m.GetStickerMessage().GetContextInfo()
	case m.GetContactMessage() != nil:
		return m.GetContactMessage().GetContextInfo()
	case m.GetLocationMessage() != nil:
		return m.GetLocationMessage().GetContextInfo()
	}
	return nil
}

func messageText(m *waE2E.Message) string {
	m, _, _ = unwrapEdit(m)
	m, _ = unwrapViewOnce(m)
	m = unwrapDocCaption(m)
	switch {
	case m.GetConversation() != "":
		return m.GetConversation()
	case m.GetExtendedTextMessage() != nil:
		return m.GetExtendedTextMessage().GetText()
	case m.GetImageMessage() != nil:
		return m.GetImageMessage().GetCaption()
	case m.GetVideoMessage() != nil:
		return m.GetVideoMessage().GetCaption()
	case m.GetDocumentMessage() != nil:
		// documento COM legenda: o WhatsApp manda documentWithCaptionMessage, mas o
		// whatsmeow já desembrulha p/ documentMessage (evt.UnwrapRaw), deixando a
		// legenda no Caption. Sem este caso, a legenda do PDF/arquivo se perdia.
		return m.GetDocumentMessage().GetCaption()
	case m.GetProductMessage() != nil:
		return productText(m.GetProductMessage())
	case m.GetOrderMessage() != nil:
		return orderText(m.GetOrderMessage())
	case getPoll(m) != nil:
		return pollText(getPoll(m))
	case m.GetInteractiveMessage() != nil:
		return interactiveText(m.GetInteractiveMessage())
	case m.GetEventMessage() != nil:
		return eventText(m.GetEventMessage())
	case m.GetContactMessage() != nil:
		return contactText(m.GetContactMessage())
	case m.GetContactsArrayMessage() != nil:
		return contactsArrayText(m.GetContactsArrayMessage())
	// Respostas a botões/listas/carrossel (o interlocutor TOCOU num botão): vira o
	// texto/opção escolhida, pra aparecer como mensagem normal dele no webhook/Chatwoot.
	case m.GetButtonsResponseMessage() != nil:
		return m.GetButtonsResponseMessage().GetSelectedDisplayText()
	case m.GetTemplateButtonReplyMessage() != nil:
		return m.GetTemplateButtonReplyMessage().GetSelectedDisplayText()
	case m.GetListResponseMessage() != nil:
		return m.GetListResponseMessage().GetTitle()
	case m.GetInteractiveResponseMessage() != nil:
		return interactiveResponseText(m.GetInteractiveResponseMessage())
	}
	return ""
}

// interactiveResponseText extrai a escolha de uma resposta interativa (nativeFlow:
// quick_reply/cta). Usa o Body se houver; senão o "id" do buttonParamsJson.
func interactiveResponseText(ir *waE2E.InteractiveResponseMessage) string {
	if b := ir.GetBody(); b != nil && b.GetText() != "" {
		return b.GetText()
	}
	if nf := ir.GetNativeFlowResponseMessage(); nf != nil {
		var p map[string]any
		if json.Unmarshal([]byte(nf.GetParamsJSON()), &p) == nil {
			if id, ok := p["id"].(string); ok && id != "" {
				return id
			}
			if dt, ok := p["display_text"].(string); ok && dt != "" {
				return dt
			}
		}
		return nf.GetParamsJSON()
	}
	return ""
}

func messageType(m *waE2E.Message) string {
	m, _, _ = unwrapEdit(m)
	m, _ = unwrapViewOnce(m)
	m = unwrapDocCaption(m)
	switch {
	case m.GetConversation() != "" || m.GetExtendedTextMessage() != nil:
		return "text"
	case m.GetImageMessage() != nil:
		return "image"
	case m.GetAudioMessage() != nil:
		return "audio"
	case m.GetVideoMessage() != nil:
		return "video"
	case m.GetDocumentMessage() != nil:
		return "document"
	case m.GetStickerMessage() != nil:
		return "sticker"
	case m.GetLocationMessage() != nil:
		return "location"
	case m.GetContactMessage() != nil || m.GetContactsArrayMessage() != nil:
		return "contact"
	case m.GetProductMessage() != nil:
		return "product"
	case m.GetOrderMessage() != nil:
		return "order"
	case getPoll(m) != nil:
		return "poll"
	case m.GetInteractiveMessage() != nil:
		return interactiveType(m.GetInteractiveMessage())
	case m.GetEventMessage() != nil:
		return "event"
	case m.GetButtonsResponseMessage() != nil || m.GetTemplateButtonReplyMessage() != nil ||
		m.GetListResponseMessage() != nil || m.GetInteractiveResponseMessage() != nil:
		return "text" // resposta de botão/lista/carrossel = escolha do interlocutor (texto)
	}
	return "unknown"
}

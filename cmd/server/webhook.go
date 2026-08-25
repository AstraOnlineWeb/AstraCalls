package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"time"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/encoding/protojson"
)

var webhookClient = &http.Client{Timeout: 10 * time.Second}

// dispatchWebhook envia um evento para a URL de webhook da sessão (se houver),
// de forma assíncrona. Formato: {session, event, timestamp, data}.
func (s *Session) dispatchWebhook(event string, data any) {
	url := s.getWebhook()
	if url == "" {
		return
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
	go func() {
		req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, url, bytes.NewReader(body))
		if err != nil {
			return
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := webhookClient.Do(req)
		if err != nil {
			s.log.Debug("webhook post failed", "url", url, "err", err)
			return
		}
		_ = resp.Body.Close()
	}()
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
	if _, viewOnce := unwrapViewOnce(evt.Message); viewOnce {
		out["viewOnce"] = true
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
	}
	return ""
}

func messageType(m *waE2E.Message) string {
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
	}
	return "unknown"
}

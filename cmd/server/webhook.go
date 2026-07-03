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
	if raw, err := protojson.Marshal(evt.Message); err == nil {
		out["raw"] = json.RawMessage(raw)
	}
	return out
}

func messageText(m *waE2E.Message) string {
	switch {
	case m.GetConversation() != "":
		return m.GetConversation()
	case m.GetExtendedTextMessage() != nil:
		return m.GetExtendedTextMessage().GetText()
	case m.GetImageMessage() != nil:
		return m.GetImageMessage().GetCaption()
	case m.GetVideoMessage() != nil:
		return m.GetVideoMessage().GetCaption()
	case m.GetProductMessage() != nil:
		return productText(m.GetProductMessage())
	case m.GetOrderMessage() != nil:
		return orderText(m.GetOrderMessage())
	}
	return ""
}

func messageType(m *waE2E.Message) string {
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
	case m.GetContactMessage() != nil:
		return "contact"
	case m.GetProductMessage() != nil:
		return "product"
	case m.GetOrderMessage() != nil:
		return "order"
	}
	return "unknown"
}

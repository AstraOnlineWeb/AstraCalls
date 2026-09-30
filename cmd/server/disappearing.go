package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"google.golang.org/protobuf/proto"
)

// POST /api/sessions/{sid}/messages/disappearing  {to, text, afterReadSeconds?}
//
// Envia um TEXTO que desaparece X segundos DEPOIS de LIDO (afterReadDuration).
// Diferente do temporário normal do WhatsApp, que conta a partir do ENVIO — aqui
// o cronômetro só começa quando o destinatário lê. Bom p/ senha/código/link
// sensível. Só texto por enquanto.
func (s *server) handleSendDisappearing(w http.ResponseWriter, r *http.Request) {
	sess := s.pairedSession(w, r.PathValue("sid"))
	if sess == nil {
		return
	}
	var b struct {
		To               string `json:"to"`
		Text             string `json:"text"`
		AfterReadSeconds uint32 `json:"afterReadSeconds"`
	}
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil || strings.TrimSpace(b.To) == "" || strings.TrimSpace(b.Text) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "to e text obrigatórios"})
		return
	}
	secs := b.AfterReadSeconds
	if secs == 0 {
		secs = 5 // default: some 5s após a leitura
	}
	initiator := waE2E.DisappearingMode_CHANGED_IN_CHAT
	trigger := waE2E.DisappearingMode_CHAT_SETTING
	msg := &waE2E.Message{
		ExtendedTextMessage: &waE2E.ExtendedTextMessage{
			Text: proto.String(b.Text),
			ContextInfo: &waE2E.ContextInfo{
				Expiration:                proto.Uint32(0),
				EphemeralSettingTimestamp: proto.Int64(time.Now().UnixMilli()),
				DisappearingMode: &waE2E.DisappearingMode{
					Initiator: initiator.Enum(),
					Trigger:   trigger.Enum(),
				},
				AfterReadDuration: proto.Uint32(secs),
			},
		},
	}
	s.send(sess, w, r, b.To, msg)
}

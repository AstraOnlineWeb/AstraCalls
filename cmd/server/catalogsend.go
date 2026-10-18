package main

import (
	"encoding/json"
	"net/http"
	"strings"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"google.golang.org/protobuf/proto"
)

// POST /api/sessions/{sid}/messages/catalog {to, body?, footer?, productId?}
//
// Mensagem de catálogo (catalog_message): um botão "Ver catálogo" que abre a
// VITRINE inteira da empresa dentro do WhatsApp (celular e Web). productId
// (retailer id) usa aquele produto como miniatura. Exige conta Business com
// catálogo. Para produtos lado a lado use /messages/carousel com botões
// {"type":"cta_catalog","id":"<produto>"}.
func (s *server) handleSendCatalog(w http.ResponseWriter, r *http.Request) {
	sess := s.pairedSession(w, r.PathValue("sid"))
	if sess == nil {
		return
	}
	var b struct {
		To        string `json:"to"`
		Body      string `json:"body"`
		Footer    string `json:"footer"`
		ProductID string `json:"productId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil || strings.TrimSpace(b.To) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "to obrigatório"})
		return
	}
	jid, err := resolveRecipient(b.To)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	phone := sess.ownNumber()
	if phone == "" {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "sessão sem número"})
		return
	}
	// business_phone_number é obrigatório: sem ele o Web abre wa.me/c/undefined e
	// o celular não faz nada. Telefone, nunca o LID.
	params := map[string]string{"business_phone_number": phone}
	if id := strings.TrimSpace(b.ProductID); id != "" {
		params["thumbnail_product_retailer_id"] = id
	}
	body := strings.TrimSpace(b.Body)
	if body == "" {
		body = "Confira nosso catálogo 👇"
	}
	im := &waE2E.InteractiveMessage{
		Body: &waE2E.InteractiveMessage_Body{Text: proto.String(body)},
		InteractiveMessage: &waE2E.InteractiveMessage_NativeFlowMessage_{
			NativeFlowMessage: &waE2E.InteractiveMessage_NativeFlowMessage{
				MessageVersion: proto.Int32(1),
				Buttons: []*waE2E.InteractiveMessage_NativeFlowMessage_NativeFlowButton{{
					Name: proto.String("catalog_message"), ButtonParamsJSON: proto.String(jsonStr(params)),
				}},
			},
		},
	}
	if f := strings.TrimSpace(b.Footer); f != "" {
		im.Footer = &waE2E.InteractiveMessage_Footer{Text: proto.String(f)}
	}
	msg := &waE2E.Message{
		InteractiveMessage: im,
		MessageContextInfo: &waE2E.MessageContextInfo{MessageSecret: newMessageSecret()},
	}
	s.sendNativeFlowNamed(sess, w, r, jid, msg, "catalog_message")
}

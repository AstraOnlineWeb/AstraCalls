package main

import (
	"encoding/json"
	"net/http"
	"strings"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"google.golang.org/protobuf/proto"
)

// Mensagens interativas (botões/listas). DUAS vias:
//   - PROTO ANTIGO: buttonsMessage / listMessage. Costuma NÃO renderizar em número
//     não-oficial (WhatsApp restringiu). Deixado disponível por completude.
//   - NATIVE FLOW (interactiveMessage): via atual (cta_url, quick_reply, copiar,
//     ligar, lista). Renderiza em muitos casos p/ não-oficial, mas SEM garantia e
//     sem suporte oficial — melhor esforço. Botão que precisa funcionar 100% deve
//     sair pela API oficial (caixa híbrida).

// POST /api/sessions/{sid}/messages/buttons {to, text, footer?, buttons:[{id,text}]}
func (s *server) handleSendButtons(w http.ResponseWriter, r *http.Request) {
	sess := s.pairedSession(w, r.PathValue("sid"))
	if sess == nil {
		return
	}
	var b struct {
		To      string `json:"to"`
		Text    string `json:"text"`
		Footer  string `json:"footer"`
		Buttons []struct {
			ID   string `json:"id"`
			Text string `json:"text"`
		} `json:"buttons"`
	}
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil || strings.TrimSpace(b.To) == "" || len(b.Buttons) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "to, text e buttons obrigatórios"})
		return
	}
	btns := make([]*waE2E.ButtonsMessage_Button, 0, len(b.Buttons))
	for i, bt := range b.Buttons {
		id := bt.ID
		if id == "" {
			id = "btn_" + itoa(i+1)
		}
		typ := waE2E.ButtonsMessage_Button_RESPONSE
		btns = append(btns, &waE2E.ButtonsMessage_Button{
			ButtonID:   proto.String(id),
			ButtonText: &waE2E.ButtonsMessage_Button_ButtonText{DisplayText: proto.String(bt.Text)},
			Type:       &typ,
		})
	}
	ht := waE2E.ButtonsMessage_EMPTY
	msg := &waE2E.Message{ButtonsMessage: &waE2E.ButtonsMessage{
		Header:      &waE2E.ButtonsMessage_Text{Text: b.Text},
		ContentText: proto.String(b.Text),
		FooterText:  proto.String(b.Footer),
		Buttons:     btns,
		HeaderType:  &ht,
	}}
	s.send(sess, w, r, b.To, msg)
}

// POST /api/sessions/{sid}/messages/list {to, text, title?, buttonText, footer?, sections:[{title,rows:[{id,title,description}]}]}
func (s *server) handleSendList(w http.ResponseWriter, r *http.Request) {
	sess := s.pairedSession(w, r.PathValue("sid"))
	if sess == nil {
		return
	}
	var b struct {
		To         string `json:"to"`
		Text       string `json:"text"`
		Title      string `json:"title"`
		ButtonText string `json:"buttonText"`
		Footer     string `json:"footer"`
		Sections   []struct {
			Title string `json:"title"`
			Rows  []struct {
				ID          string `json:"id"`
				Title       string `json:"title"`
				Description string `json:"description"`
			} `json:"rows"`
		} `json:"sections"`
	}
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil || strings.TrimSpace(b.To) == "" || len(b.Sections) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "to, buttonText e sections obrigatórios"})
		return
	}
	secs := make([]*waE2E.ListMessage_Section, 0, len(b.Sections))
	for _, sec := range b.Sections {
		rows := make([]*waE2E.ListMessage_Row, 0, len(sec.Rows))
		for i, rw := range sec.Rows {
			id := rw.ID
			if id == "" {
				id = "row_" + itoa(i+1)
			}
			rows = append(rows, &waE2E.ListMessage_Row{
				RowID: proto.String(id), Title: proto.String(rw.Title), Description: proto.String(rw.Description),
			})
		}
		secs = append(secs, &waE2E.ListMessage_Section{Title: proto.String(sec.Title), Rows: rows})
	}
	lt := waE2E.ListMessage_SINGLE_SELECT
	btnText := b.ButtonText
	if btnText == "" {
		btnText = "Ver opções"
	}
	msg := &waE2E.Message{ListMessage: &waE2E.ListMessage{
		Title: proto.String(b.Title), Description: proto.String(b.Text), ButtonText: proto.String(btnText),
		FooterText: proto.String(b.Footer), ListType: &lt, Sections: secs,
	}}
	s.send(sess, w, r, b.To, msg)
}

// POST /api/sessions/{sid}/messages/interactive
// {to, body, footer?, buttons:[{type, displayText, url?, id?, copyCode?, phoneNumber?}]}
// type ∈ quick_reply | cta_url | cta_copy | cta_call
func (s *server) handleSendInteractive(w http.ResponseWriter, r *http.Request) {
	sess := s.pairedSession(w, r.PathValue("sid"))
	if sess == nil {
		return
	}
	var b struct {
		To      string `json:"to"`
		Body    string `json:"body"`
		Footer  string `json:"footer"`
		Buttons []struct {
			Type        string `json:"type"`
			DisplayText string `json:"displayText"`
			URL         string `json:"url"`
			ID          string `json:"id"`
			CopyCode    string `json:"copyCode"`
			PhoneNumber string `json:"phoneNumber"`
		} `json:"buttons"`
	}
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil || strings.TrimSpace(b.To) == "" || len(b.Buttons) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "to, body e buttons obrigatórios"})
		return
	}
	nbtns := make([]*waE2E.InteractiveMessage_NativeFlowMessage_NativeFlowButton, 0, len(b.Buttons))
	for i, bt := range b.Buttons {
		name, params := nativeFlowButton(bt.Type, bt.DisplayText, bt.URL, bt.ID, bt.CopyCode, bt.PhoneNumber, i)
		if name == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "type inválido (quick_reply|cta_url|cta_copy|cta_call)"})
			return
		}
		nbtns = append(nbtns, &waE2E.InteractiveMessage_NativeFlowMessage_NativeFlowButton{
			Name: proto.String(name), ButtonParamsJSON: proto.String(params),
		})
	}
	msg := &waE2E.Message{InteractiveMessage: &waE2E.InteractiveMessage{
		Body:   &waE2E.InteractiveMessage_Body{Text: proto.String(b.Body)},
		Footer: &waE2E.InteractiveMessage_Footer{Text: proto.String(b.Footer)},
		InteractiveMessage: &waE2E.InteractiveMessage_NativeFlowMessage_{
			NativeFlowMessage: &waE2E.InteractiveMessage_NativeFlowMessage{
				Buttons: nbtns, MessageVersion: proto.Int32(1),
			},
		},
	}}
	s.send(sess, w, r, b.To, msg)
}

// nativeFlowButton monta (name, buttonParamsJSON) de um botão nativeFlow.
func nativeFlowButton(typ, display, url, id, copyCode, phone string, idx int) (string, string) {
	switch typ {
	case "quick_reply":
		if id == "" {
			id = "qr_" + itoa(idx+1)
		}
		return "quick_reply", jsonStr(map[string]string{"display_text": display, "id": id})
	case "cta_url":
		return "cta_url", jsonStr(map[string]string{"display_text": display, "url": url, "merchant_url": url})
	case "cta_copy":
		return "cta_copy", jsonStr(map[string]string{"display_text": display, "copy_code": copyCode, "id": copyCode})
	case "cta_call":
		return "cta_call", jsonStr(map[string]string{"display_text": display, "phone_number": phone})
	default:
		return "", ""
	}
}

func jsonStr(m map[string]string) string {
	b, _ := json.Marshal(m)
	return string(b)
}

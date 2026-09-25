package main

import (
	"crypto/rand"
	"encoding/json"
	"net/http"
	"strings"

	"go.mau.fi/whatsmeow"
	waBinary "go.mau.fi/whatsmeow/binary"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"
)

// Mensagens interativas (botões/listas) via NATIVE FLOW. Para ENTREGAR em número
// não-oficial (whatsmeow) são necessárias 3 coisas que o whatsmeow não faz sozinho
// (referência: implementação do usuário em button.js / Baileys):
//   1. messageContextInfo.messageSecret (32 bytes aleatórios) na mensagem;
//   2. o stanza extra <biz><interactive type="native_flow" v="1"><native_flow
//      v="9" name="mixed"/></interactive></biz> anexado no envio (AdditionalNodes);
//   3. reply buttons vão dentro de documentWithCaptionMessage; CTA (url/copy/call/
//      quick_reply) vão em interactiveMessage direto; lista em single_select.
// NÃO mandar <bot biz_bot="1"> em conta pessoal (o destinatário dropa).

func newMessageSecret() []byte {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return b
}

// bizNativeFlowNode é o stanza <biz> obrigatório p/ o WhatsApp renderizar o nativeFlow.
func bizNativeFlowNode() waBinary.Node {
	return waBinary.Node{
		Tag: "biz",
		Content: []waBinary.Node{{
			Tag:   "interactive",
			Attrs: waBinary.Attrs{"type": "native_flow", "v": "1"},
			Content: []waBinary.Node{{
				Tag:   "native_flow",
				Attrs: waBinary.Attrs{"v": "9", "name": "mixed"},
			}},
		}},
	}
}

// sendNativeFlow envia a mensagem interativa com o stanza <biz> anexado + resolução
// de LID (9º dígito) + id idempotente, e escreve a resposta HTTP.
func (s *server) sendNativeFlow(sess *Session, w http.ResponseWriter, r *http.Request, jid types.JID, msg *waE2E.Message) {
	nodes := []waBinary.Node{bizNativeFlowNode()}
	extra := whatsmeow.SendRequestExtra{AdditionalNodes: &nodes}
	if id := messageIDFromRequest(r); id != "" {
		extra.ID = types.MessageID(id)
	}
	resp, err := sess.sendResolvingLID(r.Context(), jid, msg, extra)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	sess.recordOutgoing(jid, resp.ID, resp.Timestamp.UnixMilli(), msg)
	writeJSON(w, http.StatusOK, map[string]any{"id": resp.ID, "to": jid.String(), "timestamp": resp.Timestamp.UnixMilli()})
}

// POST /api/sessions/{sid}/messages/buttons {to, text, footer?, buttons:[{id,text}]}
// Reply buttons (máx 3). Vão em ButtonsMessage dentro de documentWithCaptionMessage.
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
	jid, err := resolveRecipient(b.To)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
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
	msg := &waE2E.Message{
		DocumentWithCaptionMessage: &waE2E.FutureProofMessage{Message: &waE2E.Message{
			ButtonsMessage: &waE2E.ButtonsMessage{
				ContentText: proto.String(b.Text),
				FooterText:  proto.String(b.Footer),
				HeaderType:  &ht,
				Buttons:     btns,
			},
		}},
		MessageContextInfo: &waE2E.MessageContextInfo{MessageSecret: newMessageSecret()},
	}
	s.sendNativeFlow(sess, w, r, jid, msg)
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
	jid, err := resolveRecipient(b.To)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
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
	msg := &waE2E.Message{
		InteractiveMessage: &waE2E.InteractiveMessage{
			Body:   &waE2E.InteractiveMessage_Body{Text: proto.String(b.Body)},
			Footer: &waE2E.InteractiveMessage_Footer{Text: proto.String(b.Footer)},
			InteractiveMessage: &waE2E.InteractiveMessage_NativeFlowMessage_{
				NativeFlowMessage: &waE2E.InteractiveMessage_NativeFlowMessage{
					Buttons: nbtns, MessageParamsJSON: proto.String(""), MessageVersion: proto.Int32(1),
				},
			},
		},
		MessageContextInfo: &waE2E.MessageContextInfo{MessageSecret: newMessageSecret()},
	}
	s.sendNativeFlow(sess, w, r, jid, msg)
}

// POST /api/sessions/{sid}/messages/list
// {to, body, footer?, buttonText, sections:[{title, rows:[{id,title,description}]}]}
// Lista (single_select nativeFlow).
func (s *server) handleSendList(w http.ResponseWriter, r *http.Request) {
	sess := s.pairedSession(w, r.PathValue("sid"))
	if sess == nil {
		return
	}
	var b struct {
		To         string `json:"to"`
		Body       string `json:"body"`
		Text       string `json:"text"` // alias de body
		Footer     string `json:"footer"`
		ButtonText string `json:"buttonText"`
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
	jid, err := resolveRecipient(b.To)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	// monta buttonParamsJson do single_select: {title, sections:[{title, rows:[{header,title,description,id}]}]}
	type lrow struct {
		Header      string `json:"header"`
		Title       string `json:"title"`
		Description string `json:"description"`
		ID          string `json:"id"`
	}
	type lsec struct {
		Title string `json:"title"`
		Rows  []lrow `json:"rows"`
	}
	secs := make([]lsec, 0, len(b.Sections))
	for _, sec := range b.Sections {
		rows := make([]lrow, 0, len(sec.Rows))
		for i, rw := range sec.Rows {
			id := rw.ID
			if id == "" {
				id = "row_" + itoa(i+1)
			}
			rows = append(rows, lrow{Title: rw.Title, Description: rw.Description, ID: id})
		}
		secs = append(secs, lsec{Title: sec.Title, Rows: rows})
	}
	btnText := b.ButtonText
	if btnText == "" {
		btnText = "Ver opções"
	}
	params, _ := json.Marshal(map[string]any{"title": btnText, "sections": secs})
	body := b.Body
	if body == "" {
		body = b.Text
	}
	msg := &waE2E.Message{
		InteractiveMessage: &waE2E.InteractiveMessage{
			Body:   &waE2E.InteractiveMessage_Body{Text: proto.String(body)},
			Footer: &waE2E.InteractiveMessage_Footer{Text: proto.String(b.Footer)},
			InteractiveMessage: &waE2E.InteractiveMessage_NativeFlowMessage_{
				NativeFlowMessage: &waE2E.InteractiveMessage_NativeFlowMessage{
					Buttons: []*waE2E.InteractiveMessage_NativeFlowMessage_NativeFlowButton{{
						Name: proto.String("single_select"), ButtonParamsJSON: proto.String(string(params)),
					}},
					MessageParamsJSON: proto.String(""), MessageVersion: proto.Int32(1),
				},
			},
		},
		MessageContextInfo: &waE2E.MessageContextInfo{MessageSecret: newMessageSecret()},
	}
	s.sendNativeFlow(sess, w, r, jid, msg)
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
		if copyCode == "" {
			copyCode = id
		}
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

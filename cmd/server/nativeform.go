package main

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"go.mau.fi/whatsmeow"
	waBinary "go.mau.fi/whatsmeow/binary"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// =============================================================================
// Formulário NATIVO do app WhatsApp Business ("Formulários")
// =============================================================================
//
// O app Business passou a criar formulários próprios (Ferramentas comerciais →
// Formulários), ligados ao NÚMERO Business — sem WABA/Cloud API. O celular manda
// um interactiveMessage/nativeFlow com o botão "galaxy_message" apontando para o
// flow_id do formulário. Capturado de um envio real do celular (14/10/2026):
//
//	{"flow_action":"navigate","flow_message_version":"4","flow_cta":"Preencher formulário",
//	 "flow_action_payload":{"data":{...},"screen":"contact_details"},
//	 "form_type":"template","flow_id":"1615193897066865","flow_token":"<UUID>"}
//
// Aqui o AstraCalls remonta a mesma mensagem (com flow_token novo) para enviar
// pela API um formulário que a PRÓPRIA conta criou no app. Validado em 14/10/2026:
// o formulário abre no celular do cliente. O <biz> "galaxy_message" que o
// WhatsApp Web usa é recusado com 405 vindo de aparelho vinculado; com o <biz>
// "mixed" (o mesmo dos botões interativos) ou sem <biz>, entrega e abre.

// bizGalaxyFlowNode é o <biz> que o WhatsApp Web anexa a mensagens de formulário.
func bizGalaxyFlowNode() waBinary.Node {
	return waBinary.Node{
		Tag: "biz",
		Content: []waBinary.Node{{
			Tag:   "interactive",
			Attrs: waBinary.Attrs{"type": "native_flow", "v": "1"},
			Content: []waBinary.Node{{
				Tag:   "native_flow",
				Attrs: waBinary.Attrs{"name": "galaxy_message"},
			}},
		}},
	}
}

// buildNativeFormMessage monta o interactiveMessage do formulário.
func buildNativeFormMessage(flowID, body, cta, screen, formType, flowToken string, data map[string]any) (*waE2E.Message, string, error) {
	if flowToken == "" {
		flowToken = strings.ToUpper(uuid.NewString())
	}
	if cta == "" {
		cta = "Preencher formulário"
	}
	if formType == "" {
		formType = "template"
	}
	payload := map[string]any{}
	if screen != "" {
		payload["screen"] = screen
	}
	if data != nil {
		payload["data"] = data
	}
	params := map[string]any{
		"flow_action":          "navigate",
		"flow_message_version": "4",
		"flow_cta":             cta,
		"form_type":            formType,
		"flow_id":              flowID,
		"flow_token":           flowToken,
	}
	if len(payload) > 0 {
		params["flow_action_payload"] = payload
	}
	pj, err := json.Marshal(params)
	if err != nil {
		return nil, "", err
	}
	msg := &waE2E.Message{
		InteractiveMessage: &waE2E.InteractiveMessage{
			Body: &waE2E.InteractiveMessage_Body{Text: proto.String(body)},
			InteractiveMessage: &waE2E.InteractiveMessage_NativeFlowMessage_{
				NativeFlowMessage: &waE2E.InteractiveMessage_NativeFlowMessage{
					Buttons: []*waE2E.InteractiveMessage_NativeFlowMessage_NativeFlowButton{{
						Name: proto.String("galaxy_message"), ButtonParamsJSON: proto.String(string(pj)),
					}},
					MessageVersion: proto.Int32(3),
				},
			},
		},
		MessageContextInfo: &waE2E.MessageContextInfo{MessageSecret: newMessageSecret()},
	}
	return msg, flowToken, nil
}

// POST /api/sessions/{sid}/messages/native-form
// {to, flowId, body?, cta?, screen?, data?, formType?, flowToken?}
func (s *server) handleSendNativeForm(w http.ResponseWriter, r *http.Request) {
	sess := s.pairedSession(w, r.PathValue("sid"))
	if sess == nil {
		return
	}
	var b struct {
		To        string         `json:"to"`
		FlowID    string         `json:"flowId"`
		Body      string         `json:"body"`
		CTA       string         `json:"cta"`
		Screen    string         `json:"screen"`
		Data      map[string]any `json:"data"`
		FormType  string         `json:"formType"`
		FlowToken string         `json:"flowToken"`
		// Biz escolhe o nó <biz> do stanza: "mixed" (padrão, o dos botões
		// interativos), "none" ou "galaxy" (o do WhatsApp Web — o servidor recusa
		// com 405 quando vem de aparelho vinculado).
		Biz string `json:"biz"`
	}
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil || strings.TrimSpace(b.To) == "" || strings.TrimSpace(b.FlowID) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "to e flowId obrigatórios"})
		return
	}
	jid, err := resolveRecipient(b.To)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if jid.Server == types.GroupServer {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "formulário nativo só em conversa individual"})
		return
	}
	body := b.Body
	if body == "" {
		body = "Compartilhe seus dados."
	}
	msg, token, err := buildNativeFormMessage(strings.TrimSpace(b.FlowID), body, b.CTA, b.Screen, b.FormType, b.FlowToken, b.Data)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	var nodes []waBinary.Node
	switch strings.ToLower(strings.TrimSpace(b.Biz)) {
	case "none":
	case "galaxy":
		nodes = []waBinary.Node{bizGalaxyFlowNode()}
	default:
		nodes = []waBinary.Node{bizNativeFlowNode()}
	}
	extra := whatsmeow.SendRequestExtra{}
	if len(nodes) > 0 {
		extra.AdditionalNodes = &nodes
	}
	if id := messageIDFromRequest(r); id != "" {
		extra.ID = types.MessageID(id)
	}
	resp, err := sess.sendResolvingLID(r.Context(), jid, msg, extra)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	sess.recordOutgoing(jid, resp.ID, resp.Timestamp.UnixMilli(), msg)
	writeJSON(w, http.StatusOK, map[string]any{"id": resp.ID, "to": jid.String(), "flowToken": token, "timestamp": resp.Timestamp.UnixMilli()})
}

// =============================================================================
// Resposta do formulário nativo
// =============================================================================
//
// O cliente responde com interactiveResponseMessage.nativeFlowResponseMessage
// (name "galaxy_message"). Os campos vêm em paramsJSON → wa_flow_response_params
// → response_message (outro JSON, em string):
//
//	{"screens":[{"id":"contact_details","components":[
//	   {"type":"TextInput","name":"full_name","label":"","value":"Ana","isSensitive":false}, ...]}],
//	 "version":2}
//
// O flow_token não vem na resposta: sai da mensagem citada (o formulário enviado).

type nativeFormField struct {
	Screen    string `json:"screen"`
	Type      string `json:"type"`
	Name      string `json:"name"`
	Label     string `json:"label"`
	Value     any    `json:"value"`
	Sensitive bool   `json:"sensitive,omitempty"`
}

type nativeFormResponse struct {
	FlowID    string            `json:"flowId"`
	FlowName  string            `json:"flowName,omitempty"`
	FlowToken string            `json:"flowToken,omitempty"`
	Title     string            `json:"title,omitempty"`
	Fields    []nativeFormField `json:"fields"`
	Answers   map[string]any    `json:"answers"`
}

// parseNativeFormResponse extrai os campos de uma resposta de formulário nativo.
func parseNativeFormResponse(ir *waE2E.InteractiveResponseMessage) (*nativeFormResponse, bool) {
	nf := ir.GetNativeFlowResponseMessage()
	if nf == nil || nf.GetName() != "galaxy_message" {
		return nil, false
	}
	var outer struct {
		P struct {
			Title    string `json:"title"`
			FlowID   string `json:"flow_id"`
			FlowName string `json:"flow_name"`
			Response string `json:"response_message"`
		} `json:"wa_flow_response_params"`
	}
	if json.Unmarshal([]byte(nf.GetParamsJSON()), &outer) != nil || outer.P.Response == "" {
		return nil, false
	}
	var inner struct {
		Screens []struct {
			ID         string `json:"id"`
			Components []struct {
				Type        string `json:"type"`
				Name        string `json:"name"`
				Label       string `json:"label"`
				Value       any    `json:"value"`
				IsSensitive bool   `json:"isSensitive"`
			} `json:"components"`
		} `json:"screens"`
	}
	if json.Unmarshal([]byte(outer.P.Response), &inner) != nil {
		return nil, false
	}
	res := &nativeFormResponse{FlowID: outer.P.FlowID, FlowName: outer.P.FlowName, Title: outer.P.Title, Answers: map[string]any{}}
	for _, sc := range inner.Screens {
		for _, c := range sc.Components {
			if c.Name == "" {
				continue
			}
			res.Fields = append(res.Fields, nativeFormField{Screen: sc.ID, Type: c.Type, Name: c.Name, Label: c.Label, Value: c.Value, Sensitive: c.IsSensitive})
			res.Answers[c.Name] = c.Value
		}
	}
	// flow_token: da mensagem citada (o formulário que enviamos)
	if q := ir.GetContextInfo().GetQuotedMessage().GetInteractiveMessage().GetNativeFlowMessage(); q != nil {
		for _, b := range q.GetButtons() {
			var bp struct {
				FlowToken string `json:"flow_token"`
			}
			if b.GetName() == "galaxy_message" && json.Unmarshal([]byte(b.GetButtonParamsJSON()), &bp) == nil && bp.FlowToken != "" {
				res.FlowToken = bp.FlowToken
			}
		}
	}
	return res, true
}

// nativeFormFieldLabels traduz os nomes dos campos padrão dos modelos do app.
var nativeFormFieldLabels = map[string]string{
	"full_name": "Nome", "first_name": "Nome", "last_name": "Sobrenome",
	"phone_number": "Telefone", "email": "E-mail", "address": "Endereço",
	"city": "Cidade", "zip_code": "CEP", "date": "Data", "message": "Mensagem",
}

// summary devolve o texto legível da resposta (Chatwoot, webhook "message").
func (r *nativeFormResponse) summary() string {
	var b strings.Builder
	b.WriteString("📋 Formulário respondido")
	for _, f := range r.Fields {
		label := strings.TrimSpace(f.Label)
		if label == "" {
			label = nativeFormFieldLabels[f.Name]
		}
		if label == "" {
			label = f.Name
		}
		v := fmt.Sprint(f.Value)
		if f.Value == nil {
			v = ""
		}
		b.WriteString("\n" + label + ": " + v)
	}
	return b.String()
}

// =============================================================================
// Lista dos formulários nativos da conta
// =============================================================================
//
// O WhatsApp não expõe aos aparelhos vinculados a lista de formulários criados
// no app (o WhatsApp Web só abre formulários recebidos e pede ao celular a
// exportação de respostas). A lista sai do NOSSO banco: todo formulário que a
// conta enviou — pelo celular (cópia sincronizada) ou pela API — fica gravado
// com o conteúdo completo, e as respostas dos clientes também.

type nativeFormInfo struct {
	FlowID         string         `json:"flowId"`
	FlowName       string         `json:"flowName,omitempty"`
	FormType       string         `json:"formType,omitempty"`
	Body           string         `json:"body,omitempty"`
	CTA            string         `json:"cta,omitempty"`
	Screen         string         `json:"screen,omitempty"`
	Data           map[string]any `json:"data,omitempty"`
	Fields         []string       `json:"fields,omitempty"` // nomes vistos nas respostas
	SentCount      int            `json:"sentCount"`
	LastSentAt     int64          `json:"lastSentAt,omitempty"`
	ResponseCount  int            `json:"responseCount"`
	LastResponseAt int64          `json:"lastResponseAt,omitempty"`
}

// galaxyButtonParams lê os parâmetros do botão de formulário de uma mensagem.
func galaxyButtonParams(m *waE2E.Message) (map[string]any, string, bool) {
	im := m.GetInteractiveMessage()
	for _, b := range im.GetNativeFlowMessage().GetButtons() {
		if b.GetName() != "galaxy_message" {
			continue
		}
		var p map[string]any
		if json.Unmarshal([]byte(b.GetButtonParamsJSON()), &p) == nil {
			return p, im.GetBody().GetText(), true
		}
	}
	return nil, "", false
}

// listNativeForms agrega os formulários enviados pela conta e as respostas.
func (s *sessionStore) listNativeForms(ctx context.Context, sessionID string) ([]*nativeFormInfo, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT from_me, ts, raw FROM messages
		WHERE session_id = $1 AND raw IS NOT NULL
		  AND (raw::text LIKE '%galaxy_message%')
		ORDER BY ts DESC LIMIT 5000`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	forms := map[string]*nativeFormInfo{}
	fieldSeen := map[string]map[string]bool{}
	get := func(id string) *nativeFormInfo {
		f := forms[id]
		if f == nil {
			f = &nativeFormInfo{FlowID: id}
			forms[id] = f
			fieldSeen[id] = map[string]bool{}
		}
		return f
	}
	for rows.Next() {
		var fromMe bool
		var ts int64
		var raw []byte
		if err := rows.Scan(&fromMe, &ts, &raw); err != nil {
			return nil, err
		}
		var m waE2E.Message
		if (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(raw, &m) != nil {
			continue
		}
		if ir := m.GetInteractiveResponseMessage(); ir != nil {
			fr, ok := parseNativeFormResponse(ir)
			if !ok || fr.FlowID == "" || fromMe {
				continue
			}
			f := get(fr.FlowID)
			f.ResponseCount++
			if ts > f.LastResponseAt {
				f.LastResponseAt = ts
			}
			if f.FlowName == "" {
				f.FlowName = fr.FlowName
			}
			for _, fl := range fr.Fields {
				if !fieldSeen[fr.FlowID][fl.Name] {
					fieldSeen[fr.FlowID][fl.Name] = true
					f.Fields = append(f.Fields, fl.Name)
				}
			}
			continue
		}
		if !fromMe {
			continue
		}
		p, body, ok := galaxyButtonParams(&m)
		if !ok {
			continue
		}
		id, _ := p["flow_id"].(string)
		if id == "" {
			continue
		}
		f := get(id)
		f.SentCount++
		if ts > f.LastSentAt { // linhas vêm da mais nova p/ a mais antiga: a 1ª define o modelo atual
			f.LastSentAt = ts
			f.Body = body
			f.CTA, _ = p["flow_cta"].(string)
			f.FormType, _ = p["form_type"].(string)
			if pl, ok := p["flow_action_payload"].(map[string]any); ok {
				f.Screen, _ = pl["screen"].(string)
				f.Data, _ = pl["data"].(map[string]any)
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]*nativeFormInfo, 0, len(forms))
	for _, f := range forms {
		f.Body = strings.TrimPrefix(f.Body, "‎")
		f.CTA = strings.TrimPrefix(f.CTA, "‎")
		out = append(out, f)
	}
	sort.Slice(out, func(i, j int) bool {
		return max(out[i].LastSentAt, out[i].LastResponseAt) > max(out[j].LastSentAt, out[j].LastResponseAt)
	})
	return out, nil
}

// GET /api/sessions/{sid}/native-forms — formulários nativos que a conta já
// enviou (pelo celular ou pela API), com o modelo pronto para reenviar.
func (s *server) handleListNativeForms(w http.ResponseWriter, r *http.Request) {
	sess := s.sessionByID(w, r.PathValue("sid"))
	if sess == nil {
		return
	}
	if s.sessions.store == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "banco de mensagens desligado"})
		return
	}
	forms, err := s.sessions.store.listNativeForms(r.Context(), sess.id)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"forms": forms, "count": len(forms),
		"hint": "a lista vem dos formulários que a conta já enviou; formulário novo aparece aqui depois do 1º envio pelo celular",
	})
}

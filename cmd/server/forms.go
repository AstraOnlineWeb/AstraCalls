package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"os"
	"strings"
	"time"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

// Formulários (form nativo-like) para número NÃO-OFICIAL.
//
// O WhatsApp Flows (form nativo de verdade) exige WABA/conta oficial — o flow_id
// é validado contra o Meta e NÃO abre num número não-oficial. A alternativa que
// FUNCIONA é um botão que abre uma WEBVIEW em tela cheia DENTRO do WhatsApp
// (cta_url + webview_presentation=full): o cliente preenche sem sair do app.
//
// Fluxo:
//   POST /api/sessions/{sid}/messages/form  -> monta um token assinado com a
//     definição do form (campos), manda uma mensagem interativa com botão webview
//     apontando para /forms/{token} (rota PÚBLICA, sem API key — o celular do
//     cliente não tem a chave).
//   GET  /forms/{token}          -> renderiza o HTML do formulário.
//   POST /forms/{token}/submit   -> recebe o preenchimento: injeta como mensagem
//     recebida (Chatwoot + webhook "message"), dispara o evento "form_response" e
//     manda uma confirmação no WhatsApp.

// formField descreve um campo do formulário.
type formField struct {
	Name        string   `json:"name"`
	Label       string   `json:"label"`
	Type        string   `json:"type"`    // text|email|tel|number|textarea|select|date
	Options     []string `json:"options"` // para select
	Placeholder string   `json:"placeholder"`
	Required    bool     `json:"required"`
}

// formToken é o payload assinado embutido na URL do formulário.
type formToken struct {
	SID    string      `json:"sid"`
	To     string      `json:"to"`   // JID canônico do destinatário
	Chat   string      `json:"chat"` // JID do chat (=To em 1:1; grupo em grupo)
	Push   string      `json:"push"` // nome de exibição (opcional)
	Title  string      `json:"title"`
	Intro  string      `json:"intro"`
	Submit string      `json:"submit"`
	Fields []formField `json:"fields"`
	TS     int64       `json:"ts"`
}

func formSigningKey() []byte {
	k := os.Getenv("WACALLS_API_KEY")
	if k == "" {
		k = "wacalls-form"
	}
	return []byte(k)
}

func encodeFormToken(t formToken) string {
	raw, _ := json.Marshal(t)
	payload := base64.RawURLEncoding.EncodeToString(raw)
	mac := hmac.New(sha256.New, formSigningKey())
	mac.Write([]byte(payload))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return payload + "." + sig
}

func decodeFormToken(tok string) (formToken, bool) {
	var t formToken
	parts := strings.SplitN(tok, ".", 2)
	if len(parts) != 2 {
		return t, false
	}
	mac := hmac.New(sha256.New, formSigningKey())
	mac.Write([]byte(parts[0]))
	want := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(want), []byte(parts[1])) {
		return t, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return t, false
	}
	if err := json.Unmarshal(raw, &t); err != nil {
		return t, false
	}
	return t, true
}

// publicBaseURL deriva a URL pública: env WACALLS_PUBLIC_BASE_URL ou o Host da
// requisição (atrás do Traefik, com https).
func publicBaseURL(r *http.Request) string {
	if b := strings.TrimRight(os.Getenv("WACALLS_PUBLIC_BASE_URL"), "/"); b != "" {
		return b
	}
	scheme := "https"
	if p := r.Header.Get("X-Forwarded-Proto"); p != "" {
		scheme = p
	} else if r.TLS == nil {
		scheme = "http"
	}
	return scheme + "://" + r.Host
}

// POST /api/sessions/{sid}/messages/form
// {to, title?, body?, footer?, submitText?, buttonText?,
//  fields:[{name,label,type,options[],placeholder,required}]}
func (s *server) handleSendForm(w http.ResponseWriter, r *http.Request) {
	sess := s.pairedSession(w, r.PathValue("sid"))
	if sess == nil {
		return
	}
	var b struct {
		To         string      `json:"to"`
		Title      string      `json:"title"`
		Body       string      `json:"body"`
		Footer     string      `json:"footer"`
		SubmitText string      `json:"submitText"`
		ButtonText string      `json:"buttonText"`
		Fields     []formField `json:"fields"`
	}
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil || strings.TrimSpace(b.To) == "" || len(b.Fields) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "to e fields obrigatórios"})
		return
	}
	jid, err := resolveRecipient(b.To)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if b.Title == "" {
		b.Title = "Formulário"
	}
	if b.SubmitText == "" {
		b.SubmitText = "Enviar"
	}
	if b.ButtonText == "" {
		b.ButtonText = "Abrir formulário"
	}
	tok := encodeFormToken(formToken{
		SID: sess.id, To: jid.String(), Chat: jid.String(),
		Title: b.Title, Intro: b.Body, Submit: b.SubmitText,
		Fields: b.Fields, TS: time.Now().Unix(),
	})
	formURL := publicBaseURL(r) + "/forms/" + tok

	// Botão webview (cta_url + webview_presentation=full) — abre a webview no app.
	name, params := nativeFlowButton("webview", b.ButtonText, formURL, "", "", "", 0)
	msg := &waE2E.Message{
		InteractiveMessage: &waE2E.InteractiveMessage{
			Body:   &waE2E.InteractiveMessage_Body{Text: proto.String(strings.TrimSpace(b.Title + "\n\n" + b.Body))},
			Footer: &waE2E.InteractiveMessage_Footer{Text: proto.String(b.Footer)},
			InteractiveMessage: &waE2E.InteractiveMessage_NativeFlowMessage_{
				NativeFlowMessage: &waE2E.InteractiveMessage_NativeFlowMessage{
					Buttons: []*waE2E.InteractiveMessage_NativeFlowMessage_NativeFlowButton{{
						Name: proto.String(name), ButtonParamsJSON: proto.String(params),
					}},
					MessageParamsJSON: proto.String(""), MessageVersion: proto.Int32(1),
				},
			},
		},
		MessageContextInfo: &waE2E.MessageContextInfo{MessageSecret: newMessageSecret()},
	}
	s.sendNativeFlow(sess, w, r, jid, msg)
}

// GET /forms/{token} — renderiza o HTML (rota pública).
func (s *server) handleFormPage(w http.ResponseWriter, r *http.Request) {
	t, ok := decodeFormToken(r.PathValue("token"))
	if !ok {
		http.Error(w, "link inválido ou expirado", http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, renderFormHTML(t, "/forms/"+r.PathValue("token")+"/submit"))
}

// POST /forms/{token}/submit — recebe o preenchimento (rota pública).
func (s *server) handleFormSubmit(w http.ResponseWriter, r *http.Request) {
	t, ok := decodeFormToken(r.PathValue("token"))
	if !ok {
		http.Error(w, "link inválido ou expirado", http.StatusBadRequest)
		return
	}
	sess, ok := s.sessions.Get(t.SID)
	if !ok {
		http.Error(w, "sessão indisponível", http.StatusGone)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "erro ao ler formulário", http.StatusBadRequest)
		return
	}

	// Coleta ordenada + monta o resumo (texto) e o mapa de respostas.
	answers := make(map[string]string, len(t.Fields))
	var sb strings.Builder
	sb.WriteString("📋 *" + t.Title + "*\n")
	for _, f := range t.Fields {
		v := strings.TrimSpace(r.PostFormValue(f.Name))
		answers[f.Name] = v
		label := f.Label
		if label == "" {
			label = f.Name
		}
		sb.WriteString("\n*" + label + ":* " + v)
	}
	summary := sb.String()

	chat, err := resolveRecipient(t.Chat)
	if err == nil {
		// 1) injeta como mensagem RECEBIDA -> cai no Chatwoot e no webhook "message".
		synthetic := &events.Message{
			Info: types.MessageInfo{
				MessageSource: types.MessageSource{Chat: chat, Sender: chat},
				ID:            sess.client.GenerateMessageID(),
				Timestamp:     time.Now(),
				PushName:      t.Push,
			},
			Message: &waE2E.Message{Conversation: proto.String(summary)},
		}
		sess.storeMessageEvent(synthetic)
		sess.dispatchWebhook("message", sess.messagePayload(synthetic))
		go sess.chatwootPushIncoming(synthetic)
	}
	// 2) evento dedicado com as respostas estruturadas.
	sess.dispatchWebhook("form_response", map[string]any{
		"title":   t.Title,
		"from":    t.To,
		"answers": answers,
	})

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, formThanksHTML(t.Title))
}

func renderFormHTML(t formToken, action string) string {
	var fields strings.Builder
	for _, f := range t.Fields {
		req := ""
		if f.Required {
			req = " required"
		}
		lbl := html.EscapeString(f.Label)
		if f.Required {
			lbl += " <span class=req>*</span>"
		}
		ph := html.EscapeString(f.Placeholder)
		name := html.EscapeString(f.Name)
		fields.WriteString(`<label>` + lbl)
		switch f.Type {
		case "textarea":
			fields.WriteString(`<textarea name="` + name + `" rows="4" placeholder="` + ph + `"` + req + `></textarea>`)
		case "select":
			fields.WriteString(`<select name="` + name + `"` + req + `><option value="" disabled selected>` + ph + `</option>`)
			for _, o := range f.Options {
				oe := html.EscapeString(o)
				fields.WriteString(`<option value="` + oe + `">` + oe + `</option>`)
			}
			fields.WriteString(`</select>`)
		default:
			typ := f.Type
			switch typ {
			case "email", "tel", "number", "date":
			default:
				typ = "text"
			}
			fields.WriteString(`<input type="` + typ + `" name="` + name + `" placeholder="` + ph + `"` + req + `>`)
		}
		fields.WriteString(`</label>`)
	}
	return `<!doctype html><html lang=pt-BR><head><meta charset=utf-8>
<meta name=viewport content="width=device-width,initial-scale=1">
<title>` + html.EscapeString(t.Title) + `</title><style>
:root{--navy:#233E4F;--blue:#89CFF3}
*{box-sizing:border-box}body{margin:0;font-family:-apple-system,Segoe UI,Roboto,sans-serif;background:#f4f6f8;color:#1c2a33}
.wrap{max-width:520px;margin:0 auto;padding:20px 16px 48px}
h1{color:var(--navy);font-size:22px;margin:8px 0 4px}
p.intro{color:#5a6b76;margin:0 0 20px}
form{display:flex;flex-direction:column;gap:16px}
label{display:flex;flex-direction:column;gap:6px;font-weight:600;font-size:14px;color:var(--navy)}
input,select,textarea{font:inherit;font-weight:400;padding:12px 14px;border:1px solid #cdd8df;border-radius:12px;background:#fff;color:#1c2a33}
input:focus,select:focus,textarea:focus{outline:none;border-color:var(--blue);box-shadow:0 0 0 3px rgba(137,207,243,.35)}
.req{color:#e0484d}
button{margin-top:8px;padding:14px;border:0;border-radius:12px;background:var(--navy);color:#fff;font-size:16px;font-weight:700;cursor:pointer}
button:active{transform:scale(.99)}
</style></head><body><div class=wrap>
<h1>` + html.EscapeString(t.Title) + `</h1>` +
		intoSup(t.Intro) + `
<form method=post action="` + html.EscapeString(action) + `">` + fields.String() + `
<button type=submit>` + html.EscapeString(t.Submit) + `</button>
</form></div></body></html>`
}

func intoSup(s string) string {
	if strings.TrimSpace(s) == "" {
		return ""
	}
	return `<p class=intro>` + html.EscapeString(s) + `</p>`
}

func formThanksHTML(title string) string {
	return `<!doctype html><html lang=pt-BR><head><meta charset=utf-8>
<meta name=viewport content="width=device-width,initial-scale=1">
<title>Enviado</title><style>
body{margin:0;font-family:-apple-system,Segoe UI,Roboto,sans-serif;background:#f4f6f8;color:#233E4F;
display:flex;min-height:100vh;align-items:center;justify-content:center;text-align:center;padding:24px}
.card{background:#fff;border-radius:20px;padding:36px 28px;box-shadow:0 10px 40px rgba(35,62,79,.12);max-width:420px}
.ico{font-size:56px}h1{font-size:22px;margin:12px 0 6px}p{color:#5a6b76;margin:0}
</style></head><body><div class=card><div class=ico>✅</div>
<h1>Recebemos suas respostas!</h1><p>Obrigado por preencher o formulário. Já pode fechar esta janela.</p>
</div></body></html>`
}

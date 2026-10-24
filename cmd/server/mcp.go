package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

// Servidor MCP (Model Context Protocol) embutido: POST /api/mcp no transporte
// "Streamable HTTP" sem estado (uma requisição JSON-RPC por POST, resposta JSON).
// Assim Claude, Cursor, VS Code, n8n e outros clientes MCP conectam só com a URL
// e a API key — nada para instalar. Cada ferramenta vira uma chamada às rotas
// /api/* já existentes, despachada em processo pelo próprio mux; a regra de
// negócio continua num lugar só. A rota fica sob /api/, então withAuth exige a
// chave-mestra (a chave de widget não libera /api/mcp).

const (
	mcpLatestProtocol = "2025-06-18"
	mcpMaxBody        = 32 << 20 // mídia em base64 cabe aqui
	mcpMaxOutput      = 60000    // caracteres devolvidos ao modelo por chamada
)

var mcpProtocols = []string{"2025-06-18", "2025-03-26", "2024-11-05"}

// mcpInstructionsFor ajusta as instruções ao escopo da chave.
func mcpInstructionsFor(sc mcpScope) string {
	if !sc.scoped() {
		return mcpInstructions
	}
	txt := strings.Replace(mcpInstructions, "- Se houver mais de uma sessão (número de WhatsApp), chame list_sessions e passe \"session\" (ID, nome ou telefone). Com uma sessão só, pode omitir.\n",
		"- Esta conexão usa um único número de WhatsApp (veja list_sessions); não é preciso escolher sessão.\n", 1)
	if sc.ReadOnly {
		txt += "\n- Chave somente leitura: dá para consultar, mas não enviar nem alterar nada."
	}
	return txt
}

const mcpInstructions = `AstraCalls: WhatsApp (mensagens, ligações de voz e vídeo, grupos, comunidades e contatos).
- Telefones sempre com DDI + DDD, só dígitos (ex.: 5561999998888). Grupos usam o ID terminado em @g.us.
- Se houver mais de uma sessão (número de WhatsApp), chame list_sessions e passe "session" (ID, nome ou telefone). Com uma sessão só, pode omitir.
- Para ler conversas: list_chats e depois get_chat_messages; para achar algo: search_messages.
- Envios para contatos novos contam no limite do WhatsApp (get_send_limit). Evite disparos em massa.
- api_request cobre qualquer rota da API REST do AstraCalls que não tenha ferramenta própria.`

type mcpCall struct {
	Method string
	Path   string // relativo à sessão quando a ferramenta usa sessão; absoluto (/api/...) quando não
	Query  url.Values
	Body   any
}

type mcpTool struct {
	Name        string
	Title       string
	Description string
	Props       map[string]any
	Required    []string
	ReadOnly    bool
	Destructive bool
	NoSession   bool
	Build       func(a mcpArgs) (mcpCall, error)
}

// mcpArgs são os argumentos de uma chamada de ferramenta, com leitura tolerante
// (o modelo às vezes manda número como string, lista como "a, b").
type mcpArgs map[string]any

func (a mcpArgs) str(k string) string {
	switch v := a[k].(type) {
	case string:
		return strings.TrimSpace(v)
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(v)
	}
	return ""
}

func (a mcpArgs) need(k string) (string, error) {
	if v := a.str(k); v != "" {
		return v, nil
	}
	return "", fmt.Errorf("parâmetro obrigatório: %s", k)
}

func (a mcpArgs) boolean(k string) bool {
	switch v := a[k].(type) {
	case bool:
		return v
	case string:
		b, _ := strconv.ParseBool(strings.TrimSpace(v))
		return b
	}
	return false
}

func (a mcpArgs) num(k string) (float64, bool) {
	switch v := a[k].(type) {
	case float64:
		return v, true
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		return f, err == nil
	}
	return 0, false
}

func (a mcpArgs) list(k string) []string {
	var out []string
	switch v := a[k].(type) {
	case []any:
		for _, x := range v {
			if s := strings.TrimSpace(fmt.Sprint(x)); s != "" {
				out = append(out, s)
			}
		}
	case string:
		for _, s := range strings.FieldsFunc(v, func(r rune) bool { return r == ',' || r == ';' || r == '\n' }) {
			if s = strings.TrimSpace(s); s != "" {
				out = append(out, s)
			}
		}
	}
	return out
}

func (a mcpArgs) needList(k string) ([]string, error) {
	if l := a.list(k); len(l) > 0 {
		return l, nil
	}
	return nil, fmt.Errorf("parâmetro obrigatório: %s (lista)", k)
}

// copyOpt copia para body os argumentos opcionais presentes.
func (a mcpArgs) copyOpt(body map[string]any, keys ...string) {
	for _, k := range keys {
		if v, ok := a[k]; ok && v != nil && v != "" {
			body[k] = v
		}
	}
}

// ---------- esquemas ----------

func pStr(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }
func pNum(desc string) map[string]any { return map[string]any{"type": "number", "description": desc} }
func pBool(desc string) map[string]any {
	return map[string]any{"type": "boolean", "description": desc}
}
func pList(desc string) map[string]any {
	return map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": desc}
}
func pEnum(desc string, vals ...string) map[string]any {
	return map[string]any{"type": "string", "enum": vals, "description": desc}
}
func pObj(desc string) map[string]any { return map[string]any{"type": "object", "description": desc} }

var (
	argTo        = pStr("Destino: telefone com DDI+DDD só dígitos (ex.: 5561999998888) ou ID do grupo (...@g.us).")
	argPhone     = pStr("Telefone com DDI+DDD só dígitos (ex.: 5561999998888).")
	argMessageID = pStr("ID da mensagem do WhatsApp.")
	argGroupID   = pStr("ID do grupo (termina em @g.us).")
	argURL       = pStr("URL pública do arquivo.")
	argBase64    = pStr("Conteúdo em base64 (alternativa à url; pode ser data:...;base64,...).")
	argMime      = pStr("Tipo MIME (opcional; ex.: image/jpeg).")
	argQuoted    = pStr("Opcional: ID da mensagem a responder (citar).")
	argLimit     = pNum("Quantidade máxima de itens.")
	argOffset    = pNum("Quantos itens pular (paginação).")
)

func withMedia(props map[string]any) map[string]any {
	props["url"] = argURL
	props["base64"] = argBase64
	props["mimetype"] = argMime
	return props
}

func mediaBody(a mcpArgs, to string) (map[string]any, error) {
	if a.str("url") == "" && a.str("base64") == "" {
		return nil, errors.New("informe url ou base64 do arquivo")
	}
	body := map[string]any{"to": to}
	a.copyOpt(body, "url", "base64", "mimetype", "quotedMessageId")
	return body, nil
}

func pageQuery(a mcpArgs) url.Values {
	q := url.Values{}
	for _, k := range []string{"limit", "offset"} {
		if n, ok := a.num(k); ok && n > 0 {
			q.Set(k, strconv.Itoa(int(n)))
		}
	}
	return q
}

func seg(s string) string { return url.PathEscape(s) }

// mcpTools é o catálogo. Path relativo => /api/sessions/{sid} + Path.
var mcpTools = []mcpTool{
	// ----- sessão -----
	{
		Name: "list_sessions", Title: "Listar sessões", ReadOnly: true, NoSession: true,
		Description: "Lista as sessões (números de WhatsApp) do AstraCalls com ID, nome, telefone e estado da conexão.",
	},
	{
		Name: "get_send_limit", Title: "Limite de conversas novas", ReadOnly: true,
		Description: "Consulta o limite do WhatsApp para iniciar conversas novas (cota usada, restante, status de aviso/travado).",
		Build:       func(a mcpArgs) (mcpCall, error) { return mcpCall{Method: "GET", Path: "/capping"}, nil },
	},
	{
		Name: "get_restriction_status", Title: "Status de restrição", ReadOnly: true,
		Description: "Mostra se o número está com restrição/bloqueio temporário do WhatsApp.",
		Build:       func(a mcpArgs) (mcpCall, error) { return mcpCall{Method: "GET", Path: "/restriction-status"}, nil },
	},

	// ----- mensagens -----
	{
		Name: "send_text", Title: "Enviar texto",
		Description: "Envia mensagem de texto no WhatsApp.",
		Props: map[string]any{"to": argTo, "text": pStr("Texto da mensagem."), "quotedMessageId": argQuoted,
			"mentions": pList("Opcional (grupos): telefones a mencionar.")},
		Required: []string{"to", "text"},
		Build: func(a mcpArgs) (mcpCall, error) {
			to, err := a.need("to")
			if err != nil {
				return mcpCall{}, err
			}
			text, err := a.need("text")
			if err != nil {
				return mcpCall{}, err
			}
			body := map[string]any{"to": to, "text": text}
			a.copyOpt(body, "quotedMessageId")
			if m := a.list("mentions"); len(m) > 0 {
				body["mentions"] = m
			}
			return mcpCall{Method: "POST", Path: "/messages/text", Body: body}, nil
		},
	},
	mediaTool("send_image", "Enviar imagem", "Envia uma imagem (JPG/PNG) com legenda opcional.", "image", true),
	mediaTool("send_video", "Enviar vídeo", "Envia um vídeo (MP4) com legenda opcional.", "video", true),
	{
		Name: "send_audio", Title: "Enviar áudio",
		Description: "Envia um áudio. Por padrão vai como nota de voz (gravado na hora).",
		Props:       withMedia(map[string]any{"to": argTo, "ptt": pBool("Nota de voz (padrão true)."), "quotedMessageId": argQuoted}),
		Required:    []string{"to"},
		Build: func(a mcpArgs) (mcpCall, error) {
			to, err := a.need("to")
			if err != nil {
				return mcpCall{}, err
			}
			body, err := mediaBody(a, to)
			if err != nil {
				return mcpCall{}, err
			}
			body["ptt"] = true
			if _, ok := a["ptt"]; ok {
				body["ptt"] = a.boolean("ptt")
			}
			return mcpCall{Method: "POST", Path: "/messages/audio", Body: body}, nil
		},
	},
	{
		Name: "send_document", Title: "Enviar documento",
		Description: "Envia um arquivo (PDF, planilha etc.) com nome e legenda.",
		Props: withMedia(map[string]any{"to": argTo, "filename": pStr("Nome do arquivo (ex.: proposta.pdf)."),
			"caption": pStr("Legenda opcional."), "quotedMessageId": argQuoted}),
		Required: []string{"to"},
		Build: func(a mcpArgs) (mcpCall, error) {
			to, err := a.need("to")
			if err != nil {
				return mcpCall{}, err
			}
			body, err := mediaBody(a, to)
			if err != nil {
				return mcpCall{}, err
			}
			a.copyOpt(body, "filename", "caption")
			return mcpCall{Method: "POST", Path: "/messages/document", Body: body}, nil
		},
	},
	{
		Name: "send_location", Title: "Enviar localização",
		Description: "Envia uma localização no mapa.",
		Props: map[string]any{"to": argTo, "latitude": pNum("Latitude."), "longitude": pNum("Longitude."),
			"name": pStr("Nome do local (opcional)."), "address": pStr("Endereço (opcional).")},
		Required: []string{"to", "latitude", "longitude"},
		Build: func(a mcpArgs) (mcpCall, error) {
			to, err := a.need("to")
			if err != nil {
				return mcpCall{}, err
			}
			lat, ok1 := a.num("latitude")
			lng, ok2 := a.num("longitude")
			if !ok1 || !ok2 {
				return mcpCall{}, errors.New("informe latitude e longitude")
			}
			body := map[string]any{"to": to, "latitude": lat, "longitude": lng}
			a.copyOpt(body, "name", "address")
			return mcpCall{Method: "POST", Path: "/messages/location", Body: body}, nil
		},
	},
	{
		Name: "send_contact", Title: "Enviar contato",
		Description: "Envia um cartão de contato (vCard).",
		Props:       map[string]any{"to": argTo, "fullName": pStr("Nome do contato."), "phone": argPhone},
		Required:    []string{"to", "fullName", "phone"},
		Build: func(a mcpArgs) (mcpCall, error) {
			to, err := a.need("to")
			if err != nil {
				return mcpCall{}, err
			}
			name, err := a.need("fullName")
			if err != nil {
				return mcpCall{}, err
			}
			phone, err := a.need("phone")
			if err != nil {
				return mcpCall{}, err
			}
			return mcpCall{Method: "POST", Path: "/messages/contact", Body: map[string]any{
				"to": to, "contacts": []map[string]any{{"fullName": name, "phone": phone}}}}, nil
		},
	},
	{
		Name: "send_poll", Title: "Enviar enquete",
		Description: "Envia uma enquete com 2 a 12 opções.",
		Props: map[string]any{"to": argTo, "name": pStr("Pergunta da enquete."), "options": pList("Opções."),
			"multipleAnswers": pBool("Permitir várias respostas.")},
		Required: []string{"to", "name", "options"},
		Build: func(a mcpArgs) (mcpCall, error) {
			to, err := a.need("to")
			if err != nil {
				return mcpCall{}, err
			}
			name, err := a.need("name")
			if err != nil {
				return mcpCall{}, err
			}
			opts := a.list("options")
			if len(opts) < 2 {
				return mcpCall{}, errors.New("a enquete precisa de pelo menos 2 opções")
			}
			return mcpCall{Method: "POST", Path: "/messages/poll", Body: map[string]any{
				"to": to, "name": name, "options": opts, "multipleAnswers": a.boolean("multipleAnswers")}}, nil
		},
	},
	{
		Name: "send_buttons", Title: "Enviar botões",
		Description: "Envia mensagem com até 3 botões de resposta rápida.",
		Props: map[string]any{"to": argTo, "text": pStr("Texto da mensagem."),
			"buttons": map[string]any{"type": "array", "description": "Até 3 botões.", "items": map[string]any{
				"type": "object", "properties": map[string]any{"id": pStr("ID devolvido no clique."), "text": pStr("Texto do botão.")},
				"required": []string{"text"}}},
			"footer": pStr("Rodapé (opcional).")},
		Required: []string{"to", "text", "buttons"},
		Build: func(a mcpArgs) (mcpCall, error) {
			to, err := a.need("to")
			if err != nil {
				return mcpCall{}, err
			}
			raw, _ := a["buttons"].([]any)
			var buttons []map[string]any
			for _, b := range raw {
				m, _ := b.(map[string]any)
				txt := strings.TrimSpace(fmt.Sprint(m["text"]))
				if m == nil || txt == "" || txt == "<nil>" {
					continue
				}
				id := strings.TrimSpace(fmt.Sprint(m["id"]))
				if id == "" || id == "<nil>" {
					id = txt
				}
				buttons = append(buttons, map[string]any{"id": id, "text": txt})
			}
			if len(buttons) == 0 || len(buttons) > 3 {
				return mcpCall{}, errors.New("informe de 1 a 3 botões com texto")
			}
			body := map[string]any{"to": to, "text": a.str("text"), "buttons": buttons}
			a.copyOpt(body, "footer")
			return mcpCall{Method: "POST", Path: "/messages/buttons", Body: body}, nil
		},
	},
	{
		Name: "send_list", Title: "Enviar lista",
		Description: "Envia uma lista de opções (menu). Só aparece no celular.",
		Props: map[string]any{"to": argTo, "body": pStr("Texto da mensagem."), "buttonText": pStr("Texto do botão que abre a lista."),
			"sections": map[string]any{"type": "array", "description": "Seções: [{title, rows:[{id, title, description}]}].",
				"items": map[string]any{"type": "object"}},
			"footer": pStr("Rodapé (opcional).")},
		Required: []string{"to", "body", "sections"},
		Build: func(a mcpArgs) (mcpCall, error) {
			to, err := a.need("to")
			if err != nil {
				return mcpCall{}, err
			}
			if _, ok := a["sections"].([]any); !ok {
				return mcpCall{}, errors.New("sections precisa ser uma lista")
			}
			btn := a.str("buttonText")
			if btn == "" {
				btn = "Ver opções"
			}
			body := map[string]any{"to": to, "body": a.str("body"), "buttonText": btn, "sections": a["sections"]}
			a.copyOpt(body, "footer")
			return mcpCall{Method: "POST", Path: "/messages/list", Body: body}, nil
		},
	},
	{
		Name: "react", Title: "Reagir a mensagem",
		Description: "Reage a uma mensagem com um emoji (vazio remove a reação).",
		Props: map[string]any{"to": argTo, "messageId": argMessageID, "reaction": pStr("Emoji (ex.: 👍)."),
			"fromMe": pBool("A mensagem foi enviada por este número.")},
		Required: []string{"to", "messageId", "reaction"},
		Build: func(a mcpArgs) (mcpCall, error) {
			body, err := targetBody(a)
			if err != nil {
				return mcpCall{}, err
			}
			body["reaction"] = a.str("reaction")
			return mcpCall{Method: "PUT", Path: "/messages/react", Body: body}, nil
		},
	},
	{
		Name: "edit_message", Title: "Editar mensagem",
		Description: "Edita o texto de uma mensagem enviada por este número.",
		Props:       map[string]any{"to": argTo, "messageId": argMessageID, "text": pStr("Novo texto.")},
		Required:    []string{"to", "messageId", "text"},
		Build: func(a mcpArgs) (mcpCall, error) {
			body, err := targetBody(a)
			if err != nil {
				return mcpCall{}, err
			}
			body["text"] = a.str("text")
			return mcpCall{Method: "PUT", Path: "/messages/edit", Body: body}, nil
		},
	},
	{
		Name: "delete_message", Title: "Apagar mensagem", Destructive: true,
		Description: "Apaga uma mensagem para todos.",
		Props:       map[string]any{"to": argTo, "messageId": argMessageID, "fromMe": pBool("A mensagem foi enviada por este número.")},
		Required:    []string{"to", "messageId"},
		Build: func(a mcpArgs) (mcpCall, error) {
			body, err := targetBody(a)
			if err != nil {
				return mcpCall{}, err
			}
			return mcpCall{Method: "DELETE", Path: "/messages", Body: body}, nil
		},
	},
	{
		Name: "mark_read", Title: "Marcar como lida",
		Description: "Marca mensagens como lidas (tique azul).",
		Props:       map[string]any{"to": argTo, "messageIds": pList("IDs das mensagens.")},
		Required:    []string{"to", "messageIds"},
		Build: func(a mcpArgs) (mcpCall, error) {
			to, err := a.need("to")
			if err != nil {
				return mcpCall{}, err
			}
			ids, err := a.needList("messageIds")
			if err != nil {
				return mcpCall{}, err
			}
			return mcpCall{Method: "POST", Path: "/messages/seen", Body: map[string]any{"to": to, "messageIds": ids}}, nil
		},
	},
	{
		Name: "set_typing", Title: "Digitando…",
		Description: "Mostra \"digitando…\" ou \"gravando áudio…\" na conversa, ou para.",
		Props:       map[string]any{"to": argTo, "state": pEnum("Estado.", "typing", "recording", "paused")},
		Required:    []string{"to"},
		Build: func(a mcpArgs) (mcpCall, error) {
			to, err := a.need("to")
			if err != nil {
				return mcpCall{}, err
			}
			st := a.str("state")
			return mcpCall{Method: "POST", Path: "/messages/typing", Body: map[string]any{
				"to": to, "typing": st != "paused", "audio": st == "recording"}}, nil
		},
	},

	// ----- leitura de conversas -----
	{
		Name: "list_chats", Title: "Listar conversas", ReadOnly: true,
		Description: "Lista as conversas mais recentes guardadas no histórico.",
		Props:       map[string]any{"limit": argLimit, "offset": argOffset},
		Build: func(a mcpArgs) (mcpCall, error) {
			return mcpCall{Method: "GET", Path: "/chats", Query: pageQuery(a)}, nil
		},
	},
	{
		Name: "get_chat_messages", Title: "Mensagens da conversa", ReadOnly: true,
		Description: "Lê as mensagens de uma conversa (mais recentes primeiro).",
		Props:       map[string]any{"chat": pStr("Telefone, ID do grupo ou ID da conversa (de list_chats)."), "limit": argLimit, "offset": argOffset},
		Required:    []string{"chat"},
		Build: func(a mcpArgs) (mcpCall, error) {
			chat, err := a.need("chat")
			if err != nil {
				return mcpCall{}, err
			}
			return mcpCall{Method: "GET", Path: "/chats/" + seg(chat) + "/messages", Query: pageQuery(a)}, nil
		},
	},
	{
		Name: "search_messages", Title: "Buscar mensagens", ReadOnly: true,
		Description: "Busca texto nas mensagens do histórico, opcionalmente numa conversa.",
		Props:       map[string]any{"q": pStr("Texto a buscar."), "chat": pStr("Opcional: telefone ou ID da conversa."), "limit": argLimit},
		Required:    []string{"q"},
		Build: func(a mcpArgs) (mcpCall, error) {
			q, err := a.need("q")
			if err != nil {
				return mcpCall{}, err
			}
			v := pageQuery(a)
			v.Set("q", q)
			if c := a.str("chat"); c != "" {
				v.Set("chatId", c)
			}
			return mcpCall{Method: "GET", Path: "/messages/search", Query: v}, nil
		},
	},

	// ----- ligações -----
	{
		Name: "start_call", Title: "Fazer ligação",
		Description: "Liga para um número pelo WhatsApp (voz ou vídeo). A conversa por voz precisa de um atendente no painel ou de um agente de voz no WebSocket da chamada.",
		Props:       map[string]any{"phone": argPhone, "video": pBool("Chamada de vídeo."), "record": pBool("Gravar a chamada.")},
		Required:    []string{"phone"},
		Build: func(a mcpArgs) (mcpCall, error) {
			phone, err := a.need("phone")
			if err != nil {
				return mcpCall{}, err
			}
			return mcpCall{Method: "POST", Path: "/calls", Body: map[string]any{
				"phone": phone, "video": a.boolean("video"), "record": a.boolean("record")}}, nil
		},
	},
	{
		Name: "end_call", Title: "Encerrar ligação",
		Description: "Encerra uma ligação em andamento.",
		Props:       map[string]any{"callId": pStr("ID da chamada.")},
		Required:    []string{"callId"},
		Build: func(a mcpArgs) (mcpCall, error) {
			id, err := a.need("callId")
			if err != nil {
				return mcpCall{}, err
			}
			return mcpCall{Method: "DELETE", Path: "/calls/" + seg(id)}, nil
		},
	},
	{
		Name: "fake_call", Title: "Toque fantasma",
		Description: "Faz o telefone do contato tocar por alguns segundos e desliga (chama atenção sem conversa).",
		Props:       map[string]any{"phone": argPhone, "seconds": pNum("Duração do toque, 1 a 30 s (padrão 3)."), "video": pBool("Tocar como vídeo.")},
		Required:    []string{"phone"},
		Build: func(a mcpArgs) (mcpCall, error) {
			phone, err := a.need("phone")
			if err != nil {
				return mcpCall{}, err
			}
			sec, ok := a.num("seconds")
			if !ok || sec <= 0 {
				sec = 3
			}
			sec = min(30, max(1, sec))
			return mcpCall{Method: "POST", Path: "/calls/fake", Body: map[string]any{
				"phone": phone, "video": a.boolean("video"), "duration_ms": int(sec * 1000)}}, nil
		},
	},
	{
		Name: "broadcast_calls", Title: "Disparo de ligações",
		Description: "Liga para vários números e toca um áudio (ou vídeo) gravado quando atenderem. Devolve o ID do disparo.",
		Props: map[string]any{"numbers": pList("Telefones."), "audio_url": pStr("URL do áudio/vídeo a tocar."),
			"video": pBool("Ligação de vídeo (arquivo de vídeo).")},
		Required: []string{"numbers", "audio_url"},
		Build: func(a mcpArgs) (mcpCall, error) {
			nums, err := a.needList("numbers")
			if err != nil {
				return mcpCall{}, err
			}
			u, err := a.need("audio_url")
			if err != nil {
				return mcpCall{}, err
			}
			return mcpCall{Method: "POST", Path: "/broadcast", Body: map[string]any{
				"numbers": nums, "audio_url": u, "video": a.boolean("video")}}, nil
		},
	},
	{
		Name: "broadcast_status", Title: "Status do disparo", ReadOnly: true,
		Description: "Mostra o andamento de um disparo de ligações.",
		Props:       map[string]any{"campaignId": pStr("ID do disparo.")},
		Required:    []string{"campaignId"},
		Build: func(a mcpArgs) (mcpCall, error) {
			id, err := a.need("campaignId")
			if err != nil {
				return mcpCall{}, err
			}
			return mcpCall{Method: "GET", Path: "/broadcast/" + seg(id)}, nil
		},
	},
	{
		Name: "call_history", Title: "Histórico de ligações", ReadOnly: true,
		Description: "Lista as ligações feitas e recebidas.",
		Props:       map[string]any{"limit": argLimit, "offset": argOffset},
		Build: func(a mcpArgs) (mcpCall, error) {
			return mcpCall{Method: "GET", Path: "/history", Query: pageQuery(a)}, nil
		},
	},

	// ----- contatos -----
	{
		Name: "check_number", Title: "Verificar número", ReadOnly: true,
		Description: "Verifica se um telefone tem WhatsApp.",
		Props:       map[string]any{"phone": argPhone},
		Required:    []string{"phone"},
		Build: func(a mcpArgs) (mcpCall, error) {
			phone, err := a.need("phone")
			if err != nil {
				return mcpCall{}, err
			}
			return mcpCall{Method: "GET", Path: "/contacts/check", Query: url.Values{"phone": {phone}}}, nil
		},
	},
	{
		Name: "list_contacts", Title: "Listar contatos", ReadOnly: true,
		Description: "Lista os contatos salvos na agenda do número.",
		Build:       func(a mcpArgs) (mcpCall, error) { return mcpCall{Method: "GET", Path: "/contacts"}, nil },
	},
	contactTool("get_contact", "Ver contato", "Mostra nome, recado e dados de um contato.", "GET", "", true),
	contactTool("get_profile_picture", "Foto de perfil", "Devolve a URL da foto de perfil do contato (se visível).", "GET", "/picture", true),
	contactTool("block_contact", "Bloquear contato", "Bloqueia um contato.", "POST", "/block", false),
	contactTool("unblock_contact", "Desbloquear contato", "Desbloqueia um contato.", "POST", "/unblock", false),

	// ----- grupos -----
	{
		Name: "list_groups", Title: "Listar grupos", ReadOnly: true,
		Description: "Lista os grupos de que o número participa.",
		Build:       func(a mcpArgs) (mcpCall, error) { return mcpCall{Method: "GET", Path: "/groups"}, nil },
	},
	groupTool("get_group", "Ver grupo", "Mostra nome, descrição e configurações do grupo.", "GET", ""),
	groupTool("list_group_participants", "Participantes do grupo", "Lista os participantes e administradores do grupo.", "GET", "/participants"),
	groupTool("get_group_invite", "Link de convite", "Devolve o link de convite do grupo.", "GET", "/invite"),
	{
		Name: "create_group", Title: "Criar grupo",
		Description: "Cria um grupo com os participantes informados.",
		Props:       map[string]any{"name": pStr("Nome do grupo."), "participants": pList("Telefones dos participantes.")},
		Required:    []string{"name"},
		Build: func(a mcpArgs) (mcpCall, error) {
			name, err := a.need("name")
			if err != nil {
				return mcpCall{}, err
			}
			body := map[string]any{"name": name}
			if p := a.list("participants"); len(p) > 0 {
				body["participants"] = p
			}
			return mcpCall{Method: "POST", Path: "/groups", Body: body}, nil
		},
	},
	{
		Name: "update_group_participants", Title: "Gerenciar participantes",
		Description: "Adiciona, remove, promove a admin ou rebaixa participantes do grupo.",
		Props: map[string]any{"groupId": argGroupID, "action": pEnum("Ação.", "add", "remove", "promote", "demote"),
			"participants": pList("Telefones.")},
		Required: []string{"groupId", "action", "participants"},
		Build: func(a mcpArgs) (mcpCall, error) {
			gid, err := a.need("groupId")
			if err != nil {
				return mcpCall{}, err
			}
			act := a.str("action")
			switch act {
			case "add", "remove", "promote", "demote":
			default:
				return mcpCall{}, errors.New("action deve ser add, remove, promote ou demote")
			}
			p, err := a.needList("participants")
			if err != nil {
				return mcpCall{}, err
			}
			return mcpCall{Method: "POST", Path: "/groups/" + seg(gid) + "/participants/" + act, Body: map[string]any{"participants": p}}, nil
		},
	},
	{
		Name: "update_group", Title: "Editar grupo",
		Description: "Altera o nome e/ou a descrição do grupo.",
		Props:       map[string]any{"groupId": argGroupID, "name": pStr("Novo nome (opcional)."), "description": pStr("Nova descrição (opcional).")},
		Required:    []string{"groupId"},
		Build: func(a mcpArgs) (mcpCall, error) {
			gid, err := a.need("groupId")
			if err != nil {
				return mcpCall{}, err
			}
			_, hasDesc := a["description"]
			switch {
			case a.str("name") != "" && hasDesc:
				return mcpCall{}, errors.New("altere nome e descrição em chamadas separadas")
			case a.str("name") != "":
				return mcpCall{Method: "PUT", Path: "/groups/" + seg(gid) + "/subject", Body: map[string]any{"subject": a.str("name")}}, nil
			case hasDesc:
				return mcpCall{Method: "PUT", Path: "/groups/" + seg(gid) + "/description", Body: map[string]any{"description": a.str("description")}}, nil
			}
			return mcpCall{}, errors.New("informe name ou description")
		},
	},
	{
		Name: "join_group", Title: "Entrar em grupo",
		Description: "Entra num grupo pelo link ou código de convite.",
		Props:       map[string]any{"code": pStr("Link (chat.whatsapp.com/...) ou código.")},
		Required:    []string{"code"},
		Build: func(a mcpArgs) (mcpCall, error) {
			code, err := a.need("code")
			if err != nil {
				return mcpCall{}, err
			}
			return mcpCall{Method: "POST", Path: "/groups/join", Body: map[string]any{"code": code}}, nil
		},
	},
	{
		Name: "leave_group", Title: "Sair do grupo", Destructive: true,
		Description: "Sai do grupo.",
		Props:       map[string]any{"groupId": argGroupID},
		Required:    []string{"groupId"},
		Build: func(a mcpArgs) (mcpCall, error) {
			gid, err := a.need("groupId")
			if err != nil {
				return mcpCall{}, err
			}
			return mcpCall{Method: "POST", Path: "/groups/" + seg(gid) + "/leave"}, nil
		},
	},

	// ----- comunidades -----
	{
		Name: "list_communities", Title: "Listar comunidades", ReadOnly: true,
		Description: "Lista as comunidades do número.",
		Build:       func(a mcpArgs) (mcpCall, error) { return mcpCall{Method: "GET", Path: "/communities"}, nil },
	},

	// ----- genérica -----
	{
		Name: "api_request", Title: "Chamada direta à API", Destructive: true, NoSession: true,
		Description: "Chama qualquer rota da API REST do AstraCalls (/api/...). Use só quando nenhuma outra ferramenta servir. Ex.: GET /api/sessions/{id}/profile.",
		Props: map[string]any{"method": pEnum("Método HTTP.", "GET", "POST", "PUT", "DELETE"),
			"path": pStr("Caminho começando em /api/."), "query": pObj("Parâmetros de query (opcional)."), "body": pObj("Corpo JSON (opcional).")},
		Required: []string{"method", "path"},
		Build: func(a mcpArgs) (mcpCall, error) {
			m := strings.ToUpper(a.str("method"))
			switch m {
			case "GET", "POST", "PUT", "DELETE":
			default:
				return mcpCall{}, errors.New("method deve ser GET, POST, PUT ou DELETE")
			}
			p := a.str("path")
			if !strings.HasPrefix(p, "/api/") || strings.HasPrefix(p, "/api/mcp") || strings.Contains(p, "..") {
				return mcpCall{}, errors.New("path deve começar com /api/ (e não pode ser /api/mcp)")
			}
			q := url.Values{}
			if qm, ok := a["query"].(map[string]any); ok {
				for k, v := range qm {
					q.Set(k, fmt.Sprint(v))
				}
			}
			call := mcpCall{Method: m, Path: p, Query: q}
			if b, ok := a["body"]; ok && b != nil {
				call.Body = b
			}
			return call, nil
		},
	},
}

func mediaTool(name, title, desc, route string, caption bool) mcpTool {
	props := withMedia(map[string]any{"to": argTo, "quotedMessageId": argQuoted})
	if caption {
		props["caption"] = pStr("Legenda (opcional).")
	}
	return mcpTool{
		Name: name, Title: title, Description: desc + " Informe url ou base64.",
		Props: props, Required: []string{"to"},
		Build: func(a mcpArgs) (mcpCall, error) {
			to, err := a.need("to")
			if err != nil {
				return mcpCall{}, err
			}
			body, err := mediaBody(a, to)
			if err != nil {
				return mcpCall{}, err
			}
			a.copyOpt(body, "caption")
			return mcpCall{Method: "POST", Path: "/messages/" + route, Body: body}, nil
		},
	}
}

func contactTool(name, title, desc, method, suffix string, readOnly bool) mcpTool {
	return mcpTool{
		Name: name, Title: title, Description: desc, ReadOnly: readOnly,
		Props: map[string]any{"phone": argPhone}, Required: []string{"phone"},
		Build: func(a mcpArgs) (mcpCall, error) {
			phone, err := a.need("phone")
			if err != nil {
				return mcpCall{}, err
			}
			return mcpCall{Method: method, Path: "/contacts/" + seg(phone) + suffix}, nil
		},
	}
}

func groupTool(name, title, desc, method, suffix string) mcpTool {
	return mcpTool{
		Name: name, Title: title, Description: desc, ReadOnly: method == "GET",
		Props: map[string]any{"groupId": argGroupID}, Required: []string{"groupId"},
		Build: func(a mcpArgs) (mcpCall, error) {
			gid, err := a.need("groupId")
			if err != nil {
				return mcpCall{}, err
			}
			return mcpCall{Method: method, Path: "/groups/" + seg(gid) + suffix}, nil
		},
	}
}

func targetBody(a mcpArgs) (map[string]any, error) {
	to, err := a.need("to")
	if err != nil {
		return nil, err
	}
	id, err := a.need("messageId")
	if err != nil {
		return nil, err
	}
	body := map[string]any{"to": to, "messageId": id}
	if a.boolean("fromMe") {
		body["fromMe"] = true
	}
	a.copyOpt(body, "participant")
	return body, nil
}

var argSession = pStr("Sessão (número de WhatsApp) que executa: ID, nome ou telefone. Opcional se houver só uma; veja list_sessions.")

func (t mcpTool) schema(sc mcpScope) map[string]any {
	props := map[string]any{}
	for k, v := range t.Props {
		props[k] = v
	}
	if !t.NoSession && !sc.scoped() {
		props["session"] = argSession
	}
	s := map[string]any{"type": "object", "properties": props}
	if len(t.Required) > 0 {
		s["required"] = t.Required
	}
	return s
}

func (t mcpTool) descriptor(sc mcpScope) map[string]any {
	desc := t.Description
	if sc.scoped() && t.Name == "api_request" {
		desc = "Chama outra rota da API REST do AstraCalls desta sessão (/api/sessions/{id}/...). Use só quando nenhuma outra ferramenta servir."
	}
	return map[string]any{
		"name":        t.Name,
		"title":       t.Title,
		"description": desc,
		"inputSchema": t.schema(sc),
		"annotations": map[string]any{
			"title":           t.Title,
			"readOnlyHint":    t.ReadOnly,
			"destructiveHint": t.Destructive,
			"openWorldHint":   true,
		},
	}
}

// allowed diz se a ferramenta está liberada para o escopo da conexão.
func (t mcpTool) allowed(sc mcpScope) bool {
	return !sc.ReadOnly || t.ReadOnly
}

func mcpFindTool(name string) (mcpTool, bool) {
	for _, t := range mcpTools {
		if t.Name == name {
			return t, true
		}
	}
	return mcpTool{}, false
}

// ---------- JSON-RPC ----------

type mcpRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type mcpError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type mcpResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *mcpError       `json:"error,omitempty"`
}

// handleMCP: POST /api/mcp com a chave-mestra (acesso a todas as sessões). As
// chaves por sessão usam /mcp (ver mcpkeys.go).
func (s *server) handleMCP(w http.ResponseWriter, r *http.Request) {
	s.serveMCP(w, r, mcpScope{})
}

// serveMCP trata o JSON-RPC. GET devolve 405: não abrimos stream SSE de
// notificações do servidor (permitido pela especificação).
func (s *server) serveMCP(w http.ResponseWriter, r *http.Request, sc mcpScope) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "use POST (MCP Streamable HTTP)"})
		return
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, mcpMaxBody))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, mcpResponse{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &mcpError{-32700, "corpo ilegível"}})
		return
	}
	raw = bytes.TrimSpace(raw)
	if len(raw) > 0 && raw[0] == '[' { // lote (protocolos antigos)
		var reqs []mcpRequest
		if err := json.Unmarshal(raw, &reqs); err != nil {
			writeJSON(w, http.StatusBadRequest, mcpResponse{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &mcpError{-32700, "JSON inválido"}})
			return
		}
		var out []mcpResponse
		for _, req := range reqs {
			if resp, ok := s.mcpDispatch(r, req, sc); ok {
				out = append(out, resp)
			}
		}
		if len(out) == 0 {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		writeJSON(w, http.StatusOK, out)
		return
	}
	var req mcpRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, mcpResponse{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &mcpError{-32700, "JSON inválido"}})
		return
	}
	resp, ok := s.mcpDispatch(r, req, sc)
	if !ok {
		w.WriteHeader(http.StatusAccepted) // notificação: sem resposta
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// mcpDispatch trata uma mensagem; ok=false para notificações (sem id).
func (s *server) mcpDispatch(r *http.Request, req mcpRequest, sc mcpScope) (mcpResponse, bool) {
	if len(req.ID) == 0 || string(req.ID) == "null" {
		return mcpResponse{}, false
	}
	resp := mcpResponse{JSONRPC: "2.0", ID: req.ID}
	switch req.Method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		_ = json.Unmarshal(req.Params, &p)
		pv := mcpLatestProtocol
		for _, v := range mcpProtocols {
			if v == p.ProtocolVersion {
				pv = v
			}
		}
		resp.Result = map[string]any{
			"protocolVersion": pv,
			"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
			"serverInfo":      map[string]any{"name": "astracalls", "title": "AstraCalls", "version": version},
			"instructions":    mcpInstructionsFor(sc),
		}
	case "ping":
		resp.Result = map[string]any{}
	case "tools/list":
		tools := make([]map[string]any, 0, len(mcpTools))
		for _, t := range mcpTools {
			if t.allowed(sc) {
				tools = append(tools, t.descriptor(sc))
			}
		}
		resp.Result = map[string]any{"tools": tools}
	case "tools/call":
		var p struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil {
			resp.Error = &mcpError{-32602, "parâmetros inválidos"}
			break
		}
		t, ok := mcpFindTool(p.Name)
		if !ok || !t.allowed(sc) {
			resp.Error = &mcpError{-32602, "ferramenta desconhecida ou não liberada para esta chave: " + p.Name}
			break
		}
		resp.Result = s.mcpRunTool(r, t, mcpArgs(p.Arguments), sc)
	case "resources/list":
		resp.Result = map[string]any{"resources": []any{}}
	case "prompts/list":
		resp.Result = map[string]any{"prompts": []any{}}
	default:
		resp.Error = &mcpError{-32601, "método não suportado: " + req.Method}
	}
	return resp, true
}

func mcpText(text string, isErr bool) map[string]any {
	return map[string]any{"content": []map[string]any{{"type": "text", "text": text}}, "isError": isErr}
}

func (s *server) mcpRunTool(r *http.Request, t mcpTool, a mcpArgs, sc mcpScope) map[string]any {
	if a == nil {
		a = mcpArgs{}
	}
	if t.Name == "list_sessions" {
		list := s.mcpSessionList()
		if sc.scoped() {
			own := []mcpSession{}
			for _, l := range list {
				if l.ID == sc.SessionID {
					own = append(own, l)
				}
			}
			list = own
		}
		return mcpText(mcpJSON(list), false)
	}
	call, err := t.Build(a)
	if err != nil {
		return mcpText(err.Error(), true)
	}
	if sc.scoped() {
		if _, ok := s.sessions.Get(sc.SessionID); !ok {
			return mcpText("a sessão desta chave não existe mais", true)
		}
		if t.NoSession && !mcpScopedPathAllowed(sc.SessionID, call.Path) {
			return mcpText("com esta chave só é possível chamar rotas desta sessão (/api/sessions/"+sc.SessionID+"/...), exceto as de administração da conta", true)
		}
		if !t.NoSession {
			call.Path = "/api/sessions/" + seg(sc.SessionID) + call.Path
		}
	} else if !t.NoSession {
		sess, err := s.mcpResolveSession(a.str("session"))
		if err != nil {
			return mcpText(err.Error(), true)
		}
		call.Path = "/api/sessions/" + seg(sess.id) + call.Path
	}
	status, body := s.mcpInvoke(r, call)
	text := strings.TrimSpace(string(body))
	var v any
	if json.Unmarshal(body, &v) == nil {
		text = mcpJSON(v)
	}
	if text == "" {
		text = http.StatusText(status)
	}
	if status >= 400 {
		return mcpText(fmt.Sprintf("HTTP %d: %s", status, text), true)
	}
	return mcpText(text, false)
}

func mcpJSON(v any) string {
	b, _ := json.MarshalIndent(v, "", " ")
	s := string(b)
	if len(s) > mcpMaxOutput {
		s = s[:mcpMaxOutput] + "\n… (resposta cortada; use limit/offset para paginar)"
	}
	return s
}

// mcpInvoke executa a rota interna no mux, com o contexto da requisição MCP.
func (s *server) mcpInvoke(r *http.Request, call mcpCall) (int, []byte) {
	if s.apiMux == nil {
		return http.StatusInternalServerError, []byte(`{"error":"api indisponível"}`)
	}
	target := call.Path
	if len(call.Query) > 0 {
		target += "?" + call.Query.Encode()
	}
	var rd io.Reader
	if call.Body != nil {
		b, err := json.Marshal(call.Body)
		if err != nil {
			return http.StatusBadRequest, []byte(err.Error())
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(r.Context(), call.Method, target, rd)
	if err != nil {
		return http.StatusBadRequest, []byte(err.Error())
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Client-Id", "mcp")
	rec := httptest.NewRecorder()
	s.apiMux.ServeHTTP(rec, req)
	return rec.Code, rec.Body.Bytes()
}

type mcpSession struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Phone string `json:"phone,omitempty"`
	State string `json:"state"`
}

// mcpSessionList resume as sessões sem expor credenciais SIP.
func (s *server) mcpSessionList() []mcpSession {
	infos := s.sessions.infos()
	out := make([]mcpSession, 0, len(infos))
	for _, in := range infos {
		jid := in.JID
		if jid == "" {
			jid = in.LastJID
		}
		out = append(out, mcpSession{ID: in.ID, Name: in.Name, Phone: jidPhone(jid), State: in.State})
	}
	return out
}

func jidPhone(jid string) string {
	if i := strings.IndexAny(jid, ":@"); i >= 0 {
		jid = jid[:i]
	}
	return jid
}

// mcpResolveSession acha a sessão por ID, nome ou telefone; vazio = a única.
func (s *server) mcpResolveSession(ref string) (*Session, error) {
	list := s.mcpSessionList()
	if len(list) == 0 {
		return nil, errors.New("nenhuma sessão de WhatsApp cadastrada no AstraCalls")
	}
	ref = strings.TrimSpace(ref)
	pick := func(id string) (*Session, error) {
		if sess, ok := s.sessions.Get(id); ok {
			return sess, nil
		}
		return nil, errors.New("sessão não encontrada: " + ref)
	}
	if ref == "" {
		if len(list) == 1 {
			return pick(list[0].ID)
		}
		names := make([]string, 0, len(list))
		for _, l := range list {
			names = append(names, fmt.Sprintf("%s (%s, %s)", l.Name, l.Phone, l.ID))
		}
		sort.Strings(names)
		return nil, errors.New("há várias sessões; informe \"session\": " + strings.Join(names, "; "))
	}
	digits := normalizePhone(ref)
	var hits []string
	for _, l := range list {
		switch {
		case l.ID == ref:
			return pick(l.ID)
		case strings.EqualFold(l.Name, ref), digits != "" && l.Phone == digits:
			hits = append(hits, l.ID)
		}
	}
	if len(hits) == 1 {
		return pick(hits[0])
	}
	if len(hits) > 1 {
		return nil, errors.New("mais de uma sessão com esse nome; use o ID (veja list_sessions)")
	}
	return nil, errors.New("sessão não encontrada: " + ref + " (veja list_sessions)")
}

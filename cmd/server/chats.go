package main

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"go.mau.fi/whatsmeow/types"
)

// queryPage lê limit/offset da query string com defaults sensatos.
func queryPage(r *http.Request, defLimit int) (limit, offset int) {
	limit, offset = defLimit, 0
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 1000 {
			limit = n
		}
	}
	if v := r.URL.Query().Get("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			offset = n
		}
	}
	return
}

// chatsScanMax limita quantas conversas o /chats junta antes de paginar.
const chatsScanMax = 5000

// GET /api/sessions/{sid}/chats?limit=&offset=  → visão geral das conversas
//
// Conversa gravada por LID sai com o telefone (chat = PN@s.whatsapp.net, lid = o
// LID) e junta com a conversa do mesmo telefone: o histórico do pareamento vem
// quase todo por LID e o integrador (AstraChat) só cria contato com telefone a
// partir de JID @s.whatsapp.net. Cada conversa traz name/phone do contato.
func (s *server) handleListChats(w http.ResponseWriter, r *http.Request) {
	sess := s.sessionByID(w, r.PathValue("sid"))
	if sess == nil {
		return
	}
	limit, offset := queryPage(r, 500)
	raw, err := s.sessions.store.listChats(r.Context(), sess.id, chatsScanMax, 0)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	chats := sess.canonicalChats(raw)
	if offset >= len(chats) {
		chats = chats[:0]
	} else {
		chats = chats[offset:min(offset+limit, len(chats))]
	}
	writeJSON(w, http.StatusOK, chats)
}

// canonicalChats troca LID pelo telefone quando conhecido, junta as conversas
// do mesmo contato (fica a mais recente, soma as contagens) e preenche name/phone.
// A entrada já vem ordenada da mais recente para a mais antiga.
func (s *Session) canonicalChats(in []chatOverview) []chatOverview {
	out := make([]chatOverview, 0, len(in))
	idx := map[string]int{}
	for _, c := range in {
		if jid, err := types.ParseJID(c.ChatJID); err == nil && (jid.Server == types.DefaultUserServer || jid.Server == types.HiddenUserServer) {
			phone, name := s.resolvePeer(c.ChatJID)
			c.Phone, c.Name = phone, name
			if jid.Server == types.HiddenUserServer && phone != "" {
				c.LID = c.ChatJID
				c.ChatJID = types.NewJID(phone, types.DefaultUserServer).String()
			}
		}
		if i, ok := idx[c.ChatJID]; ok {
			out[i].Count += c.Count
			if out[i].LID == "" {
				out[i].LID = c.LID
			}
			continue
		}
		idx[c.ChatJID] = len(out)
		out = append(out, c)
	}
	return out
}

// chatAliases devolve os JIDs sob os quais as mensagens de um contato podem estar
// gravadas: o pedido e o par telefone/LID dele.
func (s *Session) chatAliases(chat types.JID) []string {
	out := []string{chat.String()}
	ctx := context.Background()
	switch chat.Server {
	case types.DefaultUserServer:
		if lid, err := s.client.Store.LIDs.GetLIDForPN(ctx, chat); err == nil && lid.User != "" {
			out = append(out, lid.String())
		}
	case types.HiddenUserServer:
		if pn, err := s.client.Store.LIDs.GetPNForLID(ctx, chat); err == nil && pn.User != "" {
			out = append(out, pn.String())
		}
	}
	return out
}

// GET /api/sessions/{sid}/chats/{chatId}/messages?limit=&offset=&raw=
func (s *server) handleChatMessages(w http.ResponseWriter, r *http.Request) {
	sess := s.sessionByID(w, r.PathValue("sid"))
	if sess == nil {
		return
	}
	chat, err := resolveRecipient(r.PathValue("chatId"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	limit, offset := queryPage(r, 50)
	msgs, err := s.sessions.store.listMessagesIn(r.Context(), sess.id, sess.chatAliases(chat), limit, offset, r.URL.Query().Get("raw") == "true")
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, msgs)
}

// GET /api/sessions/{sid}/messages/search?q=&chatId=&limit=
// Busca full-text (ILIKE) no corpo das mensagens do histórico.
func (s *server) handleSearchMessages(w http.ResponseWriter, r *http.Request) {
	sess := s.sessionByID(w, r.PathValue("sid"))
	if sess == nil {
		return
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "q required"})
		return
	}
	chatJID := ""
	if c := strings.TrimSpace(r.URL.Query().Get("chatId")); c != "" {
		chat, err := resolveRecipient(c)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		chatJID = chat.String()
	}
	limit, _ := queryPage(r, 50)
	msgs, err := s.sessions.store.searchMessages(r.Context(), sess.id, q, chatJID, limit)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, msgs)
}

// GET /api/sessions/{sid}/messages?chatId=&limit=&offset=&raw=
// Atalho para as mensagens de um chat via query string.
func (s *server) handleQueryMessages(w http.ResponseWriter, r *http.Request) {
	sess := s.sessionByID(w, r.PathValue("sid"))
	if sess == nil {
		return
	}
	chatID := r.URL.Query().Get("chatId")
	if chatID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "chatId required"})
		return
	}
	chat, err := resolveRecipient(chatID)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	limit, offset := queryPage(r, 50)
	msgs, err := s.sessions.store.listMessagesIn(r.Context(), sess.id, sess.chatAliases(chat), limit, offset, r.URL.Query().Get("raw") == "true")
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, msgs)
}

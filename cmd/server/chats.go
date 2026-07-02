package main

import (
	"net/http"
	"strconv"
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

// GET /api/sessions/{sid}/chats?limit=&offset=  → visão geral das conversas
func (s *server) handleListChats(w http.ResponseWriter, r *http.Request) {
	sess := s.sessionByID(w, r.PathValue("sid"))
	if sess == nil {
		return
	}
	limit, offset := queryPage(r, 100)
	chats, err := s.sessions.store.listChats(r.Context(), sess.id, limit, offset)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, chats)
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
	msgs, err := s.sessions.store.listMessages(r.Context(), sess.id, chat.String(), limit, offset, r.URL.Query().Get("raw") == "true")
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, msgs)
}

// GET /api/sessions/{sid}/messages?chatId=&limit=&offset=&raw=
// Atalho estilo WAHA para as mensagens de um chat via query string.
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
	msgs, err := s.sessions.store.listMessages(r.Context(), sess.id, chat.String(), limit, offset, r.URL.Query().Get("raw") == "true")
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, msgs)
}

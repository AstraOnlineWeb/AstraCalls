package main

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"go.mau.fi/whatsmeow"
	waBinary "go.mau.fi/whatsmeow/binary"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"
)

// POST /api/sessions/{sid}/groups/{gid}/status
//
// Posta um STATUS DO GRUPO: fica 24 h na aba Atualizações, dentro do grupo, fora
// da conversa — só os membros veem. Corpo:
//
//	texto:  {"text":"...", "backgroundColor":"#233E4F", "textColor":"#FFFFFF", "font":6}
//	imagem: {"imageUrl"|"base64":"...", "caption":"..."}
//
// No fio é uma mensagem de grupo comum embrulhada em groupStatusMessageV2, com
// contextInfo.isGroupStatus na mensagem de dentro, o messageSecret na de fora e o
// nó <meta is_group_status="true" origin="group_status"> no stanza. A imagem é
// enviada de novo a cada post (mídia reaproveitada some sem erro).
func (s *server) handleGroupStatus(w http.ResponseWriter, r *http.Request) {
	sess, gid, ok := s.group(w, r)
	if !ok {
		return
	}
	var b struct {
		Text            string `json:"text"`
		BackgroundColor string `json:"backgroundColor"`
		TextColor       string `json:"textColor"`
		Font            *int   `json:"font"`
		ImageURL        string `json:"imageUrl"`
		Base64          string `json:"base64"`
		Caption         string `json:"caption"`
		Mimetype        string `json:"mimetype"`
	}
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid body"})
		return
	}
	ctxInfo := &waE2E.ContextInfo{IsGroupStatus: proto.Bool(true)}
	var inner *waE2E.Message
	switch {
	case b.ImageURL != "" || b.Base64 != "":
		up, ok := s.uploadMedia(sess, w, r, b.Base64, b.ImageURL, whatsmeow.MediaImage)
		if !ok {
			return
		}
		mime := b.Mimetype
		if mime == "" {
			mime = "image/jpeg"
		}
		inner = &waE2E.Message{ImageMessage: &waE2E.ImageMessage{
			URL: &up.URL, DirectPath: &up.DirectPath, MediaKey: up.MediaKey,
			FileEncSHA256: up.FileEncSHA256, FileSHA256: up.FileSHA256, FileLength: proto.Uint64(up.FileLength),
			Mimetype: proto.String(mime), Caption: proto.String(b.Caption), ContextInfo: ctxInfo,
		}}
	case strings.TrimSpace(b.Text) != "":
		bg, err := parseARGB(b.BackgroundColor, 0xFF233E4F)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "backgroundColor: " + err.Error()})
			return
		}
		fg, err := parseARGB(b.TextColor, 0xFFFFFFFF)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "textColor: " + err.Error()})
			return
		}
		font := waE2E.ExtendedTextMessage_FontType(6) // SYSTEM_BOLD; o encoder só aceita o número
		if b.Font != nil {
			font = waE2E.ExtendedTextMessage_FontType(*b.Font)
		}
		inner = &waE2E.Message{ExtendedTextMessage: &waE2E.ExtendedTextMessage{
			Text: proto.String(b.Text), BackgroundArgb: proto.Uint32(bg), TextArgb: proto.Uint32(fg),
			Font: font.Enum(), ContextInfo: ctxInfo,
		}}
	default:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "text ou imageUrl/base64 obrigatório"})
		return
	}
	msg := &waE2E.Message{
		GroupStatusMessageV2: &waE2E.FutureProofMessage{Message: inner},
		MessageContextInfo:   &waE2E.MessageContextInfo{MessageSecret: newMessageSecret()},
	}
	nodes := []waBinary.Node{{Tag: "meta", Attrs: waBinary.Attrs{"is_group_status": "true", "origin": "group_status"}}}
	extra := whatsmeow.SendRequestExtra{AdditionalNodes: &nodes}
	if id := messageIDFromRequest(r); id != "" {
		extra.ID = types.MessageID(id)
	}
	resp, err := sess.client.SendMessage(r.Context(), gid, msg, extra)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": resp.ID, "to": gid.String(), "timestamp": resp.Timestamp.UnixMilli()})
}

// parseARGB aceita "#RRGGBB", "#AARRGGBB" ou número; vazio = padrão.
func parseARGB(s string, def uint32) (uint32, error) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "#")
	if s == "" {
		return def, nil
	}
	v, err := strconv.ParseUint(s, 16, 32)
	if err != nil || (len(s) != 6 && len(s) != 8) {
		return 0, errInvalidColor
	}
	if len(s) == 6 {
		v |= 0xFF000000
	}
	return uint32(v), nil
}

var errInvalidColor = jsonErr("cor inválida: use #RRGGBB ou #AARRGGBB")

type jsonErr string

func (e jsonErr) Error() string { return string(e) }

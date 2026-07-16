package main

import (
	"context"
	"encoding/json"
	"net/http"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/encoding/protojson"
)

// ---- (b) Votar numa enquete ----

// POST /api/sessions/{sid}/messages/poll-vote
// { "to": "<número|JID>", "pollMessageId": "<ID>", "selectedNames": ["Opção A"] }
// Envie selectedNames vazio para retirar o voto.
func (s *server) handlePollVote(w http.ResponseWriter, r *http.Request) {
	sess := s.pairedSession(w, r.PathValue("sid"))
	if sess == nil {
		return
	}
	var b struct {
		To            string   `json:"to"`
		PollMessageID string   `json:"pollMessageId"`
		SelectedNames []string `json:"selectedNames"`
	}
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil || b.PollMessageID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "pollMessageId obrigatório"})
		return
	}
	// reconstrói o MessageInfo da enquete a partir do que guardamos
	chatStr, senderStr, fromMe, _, err := sess.mgr.store.findMessage(r.Context(), sess.id, b.PollMessageID)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "enquete não encontrada pelo ID (precisa ter sido recebida por esta sessão)"})
		return
	}
	chat, _ := types.ParseJID(chatStr)
	sender, _ := types.ParseJID(senderStr)
	info := &types.MessageInfo{
		MessageSource: types.MessageSource{
			Chat:     chat,
			Sender:   sender,
			IsFromMe: fromMe,
			IsGroup:  chat.Server == types.GroupServer,
		},
		ID: b.PollMessageID,
	}
	msg, err := sess.client.BuildPollVote(r.Context(), info, b.SelectedNames)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	// o voto vai para o chat da enquete; aceita 'to' como override
	target := chat
	if b.To != "" {
		if j, e := resolveRecipient(b.To); e == nil {
			target = j
		}
	}
	s.sendTo(sess, w, r, target, msg)
}

// ---- (c) Receber votos ----

// handleIncomingPollVote decodifica um PollUpdateMessage, mapeia os hashes de
// volta para os nomes das opções e encaminha (webhook poll_vote + Chatwoot).
func (s *Session) handleIncomingPollVote(evt *events.Message) {
	ctx := s.mgr.appCtx
	vote, err := s.client.DecryptPollVote(ctx, evt)
	if err != nil {
		s.log.Warn("decrypt poll vote falhou", "err", err)
		return
	}
	pollID := evt.Message.GetPollUpdateMessage().GetPollCreationMessageKey().GetID()

	// mapeia hashes -> nomes usando a enquete original guardada
	var names []string
	if _, _, _, raw, e := s.mgr.store.findMessage(ctx, s.id, pollID); e == nil && len(raw) > 0 {
		var pm waE2E.Message
		if protojson.Unmarshal(raw, &pm) == nil {
			if p := getPoll(&pm); p != nil {
				names = matchVoteOptions(vote.GetSelectedOptions(), pollOptionNames(p))
			}
		}
	}

	voter := evt.Info.PushName
	if voter == "" {
		voter = s.realPhone(evt.Info.Sender)
	}
	s.dispatchWebhook("poll_vote", map[string]any{
		"pollMessageId": pollID,
		"voter":         evt.Info.Sender.String(),
		"voterPhone":    s.realPhone(evt.Info.Sender),
		"chat":          evt.Info.Chat.String(),
		"selectedNames": names,
		"timestamp":     evt.Info.Timestamp.UnixMilli(),
	})

	if cfg := s.getChatwoot(); cfg.valid() {
		s.chatwootPushVote(cfg, evt, pollVoteText(voter, names))
	}
}

// chatwootPushVote posta o texto do voto na conversa do chat (1:1 ou grupo).
func (s *Session) chatwootPushVote(cfg ChatwootConfig, evt *events.Message, text string) {
	var contactID int
	var sourceID string
	var err error
	switch evt.Info.Chat.Server {
	case types.GroupServer:
		if !cfg.Groups {
			return
		}
		group := evt.Info.Chat
		name := group.String()
		if gi, e := s.client.GetGroupInfo(context.Background(), group); e == nil && gi.Name != "" {
			name = gi.Name
		}
		contactID, sourceID, err = cfg.ensureContact(group.String(), "", name, "")
	case types.DefaultUserServer, types.HiddenUserServer:
		chat := evt.Info.Chat
		phone := chat.User
		if chat.Server != types.DefaultUserServer {
			if evt.Info.SenderAlt.Server == types.DefaultUserServer && evt.Info.SenderAlt.User != "" {
				phone = evt.Info.SenderAlt.User
			} else {
				phone = s.realPhone(chat)
			}
		}
		contactID, sourceID, err = cfg.ensureContact(phone+"@"+types.DefaultUserServer, phone, phone, "")
	default:
		return
	}
	if err != nil {
		s.log.Error("chatwoot: voto - contato falhou", "err", err)
		return
	}
	convID, err := cfg.ensureConversation(contactID, sourceID)
	if err != nil {
		return
	}
	_ = cfg.postText(convID, text, cwIncoming, "", "", 0)
}

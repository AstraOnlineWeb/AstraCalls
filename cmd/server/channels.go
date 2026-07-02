package main

import (
	"net/http"
	"strconv"
	"strings"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"
)

// resolveNewsletterJID aceita "123...@newsletter" ou só o id.
func resolveNewsletterJID(id string) (types.JID, error) {
	id = strings.TrimSpace(id)
	if strings.Contains(id, "@") {
		return types.ParseJID(id)
	}
	return types.NewJID(id, types.NewsletterServer), nil
}

func newsletterJSON(n *types.NewsletterMetadata) map[string]any {
	out := map[string]any{
		"jid":         n.ID.String(),
		"name":        n.ThreadMeta.Name.Text,
		"description": n.ThreadMeta.Description.Text,
		"subscribers": n.ThreadMeta.SubscriberCount,
		"invite":      n.ThreadMeta.InviteCode,
	}
	if n.ViewerMeta != nil {
		out["role"] = string(n.ViewerMeta.Role)
		out["muted"] = n.ViewerMeta.Mute
	}
	return out
}

// channel carrega a sessão pareada + o JID do canal do path.
func (s *server) channel(w http.ResponseWriter, r *http.Request) (*Session, types.JID, bool) {
	sess := s.pairedSession(w, r.PathValue("sid"))
	if sess == nil {
		return nil, types.JID{}, false
	}
	jid, err := resolveNewsletterJID(r.PathValue("id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return nil, types.JID{}, false
	}
	return sess, jid, true
}

// GET /api/sessions/{sid}/channels  → canais que a conta segue
func (s *server) handleListChannels(w http.ResponseWriter, r *http.Request) {
	sess := s.pairedSession(w, r.PathValue("sid"))
	if sess == nil {
		return
	}
	list, err := sess.client.GetSubscribedNewsletters(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	out := make([]map[string]any, 0, len(list))
	for _, n := range list {
		out = append(out, newsletterJSON(n))
	}
	writeJSON(w, http.StatusOK, out)
}

// GET /api/sessions/{sid}/channels/{id}
func (s *server) handleChannelInfo(w http.ResponseWriter, r *http.Request) {
	sess, jid, ok := s.channel(w, r)
	if !ok {
		return
	}
	n, err := sess.client.GetNewsletterInfo(r.Context(), jid)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, newsletterJSON(n))
}

// POST /api/sessions/{sid}/channels/{id}/follow  e /unfollow
func (s *server) handleChannelFollow(follow bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sess, jid, ok := s.channel(w, r)
		if !ok {
			return
		}
		var err error
		if follow {
			err = sess.client.FollowNewsletter(r.Context(), jid)
		} else {
			err = sess.client.UnfollowNewsletter(r.Context(), jid)
		}
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "following": follow})
	}
}

// POST /api/sessions/{sid}/channels/{id}/mute  e /unmute
func (s *server) handleChannelMute(mute bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sess, jid, ok := s.channel(w, r)
		if !ok {
			return
		}
		if err := sess.client.NewsletterToggleMute(r.Context(), jid, mute); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "muted": mute})
	}
}

// GET /api/sessions/{sid}/channels/{id}/messages?limit=20
func (s *server) handleChannelMessages(w http.ResponseWriter, r *http.Request) {
	sess, jid, ok := s.channel(w, r)
	if !ok {
		return
	}
	count := 20
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			count = n
		}
	}
	msgs, err := sess.client.GetNewsletterMessages(r.Context(), jid, &whatsmeow.GetNewsletterMessagesParams{Count: count})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	out := make([]map[string]any, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, map[string]any{
			"serverId":  m.MessageServerID,
			"timestamp": m.Timestamp.UnixMilli(),
			"views":     m.ViewsCount,
			"type":      m.Type,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

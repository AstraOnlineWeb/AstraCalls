package main

import (
	"encoding/json"
	"net/http"
	"strings"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"
)

// Comunidades do WhatsApp: no protocolo uma comunidade é um grupo "pai"
// (IsParent) com grupos filhos vinculados; o grupo de avisos é criado pelo
// servidor junto com a comunidade (IsDefaultSubGroup).

// community carrega a sessão pareada + o JID da comunidade do path.
func (s *server) community(w http.ResponseWriter, r *http.Request) (*Session, types.JID, bool) {
	sess := s.pairedSession(w, r.PathValue("sid"))
	if sess == nil {
		return nil, types.JID{}, false
	}
	cid, err := resolveGroupJID(r.PathValue("cid"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return nil, types.JID{}, false
	}
	return sess, cid, true
}

func subGroupJSON(g *types.GroupLinkTarget) map[string]any {
	return map[string]any{
		"jid":                 g.JID.String(),
		"id":                  g.JID.String(),
		"name":                g.Name,
		"subject":             g.Name,
		"isAnnouncementGroup": g.IsDefaultSubGroup,
	}
}

// POST /api/sessions/{sid}/communities  {name, description?, participants?, approval?}
func (s *server) handleCreateCommunity(w http.ResponseWriter, r *http.Request) {
	sess := s.pairedSession(w, r.PathValue("sid"))
	if sess == nil {
		return
	}
	var b struct {
		Name         string   `json:"name"`
		Subject      string   `json:"subject"`
		Description  string   `json:"description"`
		Participants []string `json:"participants"`
		Approval     bool     `json:"approval"` // entrada só com aprovação de admin
	}
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	name := strings.TrimSpace(firstNonEmptyOf(b.Name, b.Subject))
	if name == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name required"})
		return
	}
	parts, err := resolveParticipants(b.Participants)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	req := whatsmeow.ReqCreateGroup{Name: name, Participants: parts}
	req.IsParent = true
	if b.Approval {
		req.DefaultMembershipApprovalMode = "request_required"
	}
	g, err := sess.client.CreateGroup(r.Context(), req)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if d := strings.TrimSpace(b.Description); d != "" {
		if err := sess.client.SetGroupTopic(r.Context(), g.JID, "", "", d); err == nil {
			g.Topic = d
		}
	}
	writeJSON(w, http.StatusOK, sess.groupJSON(g))
}

// GET /api/sessions/{sid}/communities  → comunidades em que a conta está
func (s *server) handleListCommunities(w http.ResponseWriter, r *http.Request) {
	sess := s.pairedSession(w, r.PathValue("sid"))
	if sess == nil {
		return
	}
	groups, err := sess.client.GetJoinedGroups(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	out := make([]map[string]any, 0)
	for _, g := range groups {
		if g.IsParent {
			out = append(out, sess.groupJSON(g))
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// GET /api/sessions/{sid}/communities/{cid}  → info + grupos vinculados
func (s *server) handleCommunityInfo(w http.ResponseWriter, r *http.Request) {
	sess, cid, ok := s.community(w, r)
	if !ok {
		return
	}
	g, err := sess.client.GetGroupInfo(r.Context(), cid)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	out := sess.groupJSON(g)
	if !g.IsParent {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "não é uma comunidade (use /groups/{gid})"})
		return
	}
	subs, err := sess.client.GetSubGroups(r.Context(), cid)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	list := make([]map[string]any, 0, len(subs))
	for _, sg := range subs {
		list = append(list, subGroupJSON(sg))
	}
	out["groups"] = list
	writeJSON(w, http.StatusOK, out)
}

// GET /api/sessions/{sid}/communities/{cid}/groups  → grupos vinculados
func (s *server) handleCommunityGroups(w http.ResponseWriter, r *http.Request) {
	sess, cid, ok := s.community(w, r)
	if !ok {
		return
	}
	subs, err := sess.client.GetSubGroups(r.Context(), cid)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	list := make([]map[string]any, 0, len(subs))
	for _, sg := range subs {
		list = append(list, subGroupJSON(sg))
	}
	writeJSON(w, http.StatusOK, list)
}

// POST /api/sessions/{sid}/communities/{cid}/groups  {name, participants?}
// cria um grupo novo já dentro da comunidade.
func (s *server) handleCommunityCreateGroup(w http.ResponseWriter, r *http.Request) {
	sess, cid, ok := s.community(w, r)
	if !ok {
		return
	}
	var b struct {
		Name         string   `json:"name"`
		Subject      string   `json:"subject"`
		Participants []string `json:"participants"`
	}
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	name := strings.TrimSpace(firstNonEmptyOf(b.Name, b.Subject))
	if name == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name required"})
		return
	}
	parts, err := resolveParticipants(b.Participants)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	req := whatsmeow.ReqCreateGroup{Name: name, Participants: parts}
	req.LinkedParentJID = cid
	g, err := sess.client.CreateGroup(r.Context(), req)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, sess.groupJSON(g))
}

// POST /api/sessions/{sid}/communities/{cid}/groups/link|unlink  {groups:[gid...]}
// (aceita também {group:"gid"}). Resultado por grupo: falha de um não aborta os outros.
func (s *server) handleCommunityLink(link bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sess, cid, ok := s.community(w, r)
		if !ok {
			return
		}
		var b struct {
			Groups []string `json:"groups"`
			Group  string   `json:"group"`
		}
		if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
			return
		}
		if b.Group != "" {
			b.Groups = append(b.Groups, b.Group)
		}
		if len(b.Groups) == 0 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "groups required"})
			return
		}
		results := make([]map[string]any, 0, len(b.Groups))
		okCount := 0
		for _, raw := range b.Groups {
			res := map[string]any{"group": raw, "ok": false}
			gid, err := resolveGroupJID(raw)
			if err == nil {
				res["group"] = gid.String()
				if link {
					err = sess.client.LinkGroup(r.Context(), cid, gid)
				} else {
					err = sess.client.UnlinkGroup(r.Context(), cid, gid)
				}
			}
			if err != nil {
				res["error"] = err.Error()
			} else {
				res["ok"] = true
				okCount++
			}
			results = append(results, res)
		}
		status := http.StatusOK
		if okCount == 0 {
			status = http.StatusBadGateway
		}
		writeJSON(w, status, map[string]any{"ok": okCount == len(results), "results": results})
	}
}

// GET /api/sessions/{sid}/communities/{cid}/participants  → membros de todos os grupos
func (s *server) handleCommunityParticipants(w http.ResponseWriter, r *http.Request) {
	sess, cid, ok := s.community(w, r)
	if !ok {
		return
	}
	jids, err := sess.client.GetLinkedGroupsParticipants(r.Context(), cid)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	out := make([]map[string]any, 0, len(jids))
	for _, j := range jids {
		p := types.GroupParticipant{JID: j}
		if j.Server == types.HiddenUserServer {
			p.LID = j
		} else {
			p.PhoneNumber = j
		}
		out = append(out, sess.participantJSON(p))
	}
	writeJSON(w, http.StatusOK, out)
}

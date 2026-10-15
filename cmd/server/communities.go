package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"go.mau.fi/whatsmeow"
	waBinary "go.mau.fi/whatsmeow/binary"
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

// createCommunity monta o <create> no formato do app/WA Web. O CreateGroup do
// whatsmeow com IsParent manda nós de grupo comum (ephemeral, member_add_mode,
// membership_approval_mode) e o servidor responde 400 bad-request.
// Mesmo formato do communityCreate do Baileys (validado em produção por eles).
func createCommunity(ctx context.Context, cli *whatsmeow.Client, name, description string) (types.JID, error) {
	if description == "" {
		description = name
	}
	in := cli.DangerousInternals()
	content := []waBinary.Node{
		{Tag: "description", Attrs: waBinary.Attrs{"id": in.GenerateRequestID()}, Content: []waBinary.Node{{Tag: "body", Content: []byte(description)}}},
		{Tag: "parent", Attrs: waBinary.Attrs{"default_membership_approval_mode": "request_required"}},
		{Tag: "allow_non_admin_sub_group_creation"},
		{Tag: "create_general_chat"},
	}
	resp, err := in.SendIQ(ctx, whatsmeow.DangerousInfoQuery{
		Namespace: "w:g2",
		Type:      "set",
		To:        types.GroupServerJID,
		Content: []waBinary.Node{{
			Tag:     "create",
			Attrs:   waBinary.Attrs{"subject": name},
			Content: content,
		}},
	})
	if err != nil {
		return types.JID{}, err
	}
	g, ok := resp.GetOptionalChildByTag("group")
	if !ok {
		return types.JID{}, fmt.Errorf("resposta sem <group> ao criar comunidade")
	}
	id := g.AttrGetter().OptionalString("id")
	if id == "" {
		return types.JID{}, fmt.Errorf("resposta sem id ao criar comunidade")
	}
	return resolveGroupJID(id)
}

// POST /api/sessions/{sid}/communities  {name, description?, participants?}
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
	cid, err := createCommunity(r.Context(), sess.client, name, strings.TrimSpace(b.Description))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	out := map[string]any{}
	if len(parts) > 0 {
		res, err := sess.communityAdd(r.Context(), cid, parts)
		if err != nil {
			out["participantsError"] = err.Error()
		} else {
			out["participantsResult"] = res
		}
	}
	g, err := sess.client.GetGroupInfo(r.Context(), cid)
	if err != nil {
		out["jid"], out["id"], out["name"], out["isCommunity"] = cid.String(), cid.String(), name, true
		writeJSON(w, http.StatusOK, out)
		return
	}
	for k, v := range sess.groupJSON(g) {
		out[k] = v
	}
	writeJSON(w, http.StatusOK, out)
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
	sess.canonicalParticipants(r.Context(), parts)
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
		var sent []types.JID
		for _, raw := range b.Groups {
			res := map[string]any{"group": raw, "ok": false}
			gid, err := resolveGroupJID(raw)
			if err == nil {
				res["group"] = gid.String()
				err = sess.communityLinkIQ(r.Context(), cid, gid, link)
			}
			if err != nil {
				res["error"] = err.Error()
			} else {
				sent = append(sent, gid)
			}
			results = append(results, res)
		}
		// o servidor pode responder OK sem aplicar: confere o estado real
		okCount := 0
		if len(sent) > 0 {
			linked := map[string]bool{}
			if subs, err := sess.client.GetSubGroups(r.Context(), cid); err == nil {
				for _, sg := range subs {
					linked[sg.JID.String()] = true
				}
				for _, res := range results {
					g, _ := res["group"].(string)
					if _, failed := res["error"]; failed {
						continue
					}
					if linked[g] == link {
						res["ok"] = true
						okCount++
					} else if link {
						res["error"] = "WhatsApp aceitou o pedido mas o grupo não aparece vinculado"
					} else {
						res["error"] = "WhatsApp aceitou o pedido mas o grupo continua vinculado"
					}
				}
			} else {
				for _, res := range results {
					if _, failed := res["error"]; !failed {
						res["ok"] = true
						okCount++
					}
				}
			}
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

// communityLinkIQ vincula/desvincula um grupo. Mesmo stanza do whatsmeow, mas
// lendo a resposta: erro por grupo vem como atributo dentro de <links>/<unlink>.
func (s *Session) communityLinkIQ(ctx context.Context, cid, gid types.JID, link bool) error {
	var node waBinary.Node
	if link {
		node = waBinary.Node{Tag: "links", Content: []waBinary.Node{{
			Tag:     "link",
			Attrs:   waBinary.Attrs{"link_type": "sub_group"},
			Content: []waBinary.Node{{Tag: "group", Attrs: waBinary.Attrs{"jid": gid}}},
		}}}
	} else {
		node = waBinary.Node{Tag: "unlink", Attrs: waBinary.Attrs{"unlink_type": "sub_group"},
			Content: []waBinary.Node{{Tag: "group", Attrs: waBinary.Attrs{"jid": gid}}}}
	}
	resp, err := s.client.DangerousInternals().SendIQ(ctx, whatsmeow.DangerousInfoQuery{
		Namespace: "w:g2", Type: "set", To: cid, Content: []waBinary.Node{node},
	})
	if err != nil {
		return err
	}
	s.log.Info("community link resp", "link", link, "community", cid.String(), "group", gid.String(), "resp", fmt.Sprintf("%v", resp))
	return firstNodeError(resp)
}

// firstNodeError procura um atributo error em qualquer nó da resposta.
func firstNodeError(n *waBinary.Node) error {
	if n == nil {
		return nil
	}
	if code := n.AttrGetter().OptionalString("error"); code != "" && n.Tag != "iq" {
		return fmt.Errorf("WhatsApp recusou (%s error=%s)", n.Tag, code)
	}
	for _, c := range n.GetChildren() {
		if err := firstNodeError(&c); err != nil {
			return err
		}
	}
	return nil
}

// communityAdd adiciona membros à comunidade. O app faz isso pelo grupo de
// avisos (o add direto no grupo pai volta 400).
func (s *Session) communityAdd(ctx context.Context, cid types.JID, parts []types.JID) ([]map[string]any, error) {
	subs, err := s.client.GetSubGroups(ctx, cid)
	if err != nil {
		return nil, err
	}
	var ann types.JID
	for _, sg := range subs {
		if sg.IsDefaultSubGroup {
			ann = sg.JID
			break
		}
	}
	if ann.IsEmpty() {
		return nil, fmt.Errorf("grupo de avisos da comunidade não encontrado")
	}
	s.canonicalParticipants(ctx, parts)
	res, err := s.client.UpdateGroupParticipants(ctx, ann, parts, whatsmeow.ParticipantChangeAdd)
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(res))
	for _, p := range res {
		row := map[string]any{"jid": p.JID.String(), "phone": s.participantPhone(p.JID, p.PhoneNumber), "ok": p.Error == 0}
		if p.Error != 0 {
			row["error"] = p.Error
		}
		out = append(out, row)
	}
	return out, nil
}

// POST /api/sessions/{sid}/communities/{cid}/participants/add  {participants:[...]}
func (s *server) handleCommunityAddParticipants(w http.ResponseWriter, r *http.Request) {
	sess, cid, ok := s.community(w, r)
	if !ok {
		return
	}
	var b struct {
		Participants []string `json:"participants"`
	}
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil || len(b.Participants) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "participants required"})
		return
	}
	parts, err := resolveParticipants(b.Participants)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	res, err := sess.communityAdd(r.Context(), cid, parts)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "participants": res})
}

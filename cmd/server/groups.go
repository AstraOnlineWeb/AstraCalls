package main

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"
)

// resolveGroupJID aceita "123...@g.us" ou só o id numérico do grupo.
func resolveGroupJID(gid string) (types.JID, error) {
	gid = strings.TrimSpace(gid)
	if strings.Contains(gid, "@") {
		return types.ParseJID(gid)
	}
	return types.NewJID(gid, types.GroupServer), nil
}

// resolveParticipants converte uma lista de números/JIDs em []types.JID.
func resolveParticipants(list []string) ([]types.JID, error) {
	out := make([]types.JID, 0, len(list))
	for _, p := range list {
		jid, err := resolveRecipient(p)
		if err != nil {
			return nil, err
		}
		out = append(out, jid)
	}
	return out, nil
}

// participantJSON serializa um participante com identidade completa: JID de
// envio, telefone real (PN), LID e nome do contato. Pedido do AstraChat (lista
// lateral + menção): só o JID @lid virava "+608386..." na tela.
func (s *Session) participantJSON(p types.GroupParticipant) map[string]any {
	phone := p.PhoneNumber.User
	if phone == "" {
		phone, _ = s.resolvedPhone(p.JID) // "" quando o LID não resolve
	}
	lid := p.LID
	if lid.IsEmpty() && p.JID.Server == types.HiddenUserServer {
		lid = p.JID
	}
	name, push := s.contactNames(p.JID, p.PhoneNumber, p.LID)
	if name == "" && p.DisplayName != "" {
		name = p.DisplayName
	}
	number := p.JID.User
	if phone != "" {
		number = phone
	}
	out := map[string]any{
		"jid":          p.JID.String(),
		"number":       number, // telefone quando conhecido; senão o user do JID (compat)
		"phone":        phone,  // SÓ telefone real; "" se o LID não resolver
		"lid":          "",
		"name":         name,
		"pushName":     push,
		"isAdmin":      p.IsAdmin,
		"isSuperAdmin": p.IsSuperAdmin,
		"admin":        p.IsAdmin || p.IsSuperAdmin,
		// aliases WAHA
		"id":   waChatID(p.JID),
		"pn":   number,
		"role": waRole(p.IsAdmin, p.IsSuperAdmin),
	}
	if !lid.IsEmpty() {
		out["lid"] = lid.String()
	}
	return out
}

// contactNames procura o nome do contato por qualquer um dos JIDs (os contatos
// costumam estar indexados pelo PN, mas em grupos LID só temos o @lid). Devolve
// (melhor nome, pushName).
func (s *Session) contactNames(jids ...types.JID) (name, push string) {
	for _, j := range jids {
		if j.IsEmpty() {
			continue
		}
		ci, err := s.client.Store.Contacts.GetContact(context.Background(), j)
		if err != nil || !ci.Found {
			continue
		}
		if push == "" {
			push = ci.PushName
		}
		if n := firstNonEmptyOf(ci.FullName, ci.PushName, ci.FirstName, ci.BusinessName); n != "" && name == "" {
			name = n
		}
	}
	// fallback: pelo PN derivado do LID
	if name == "" {
		for _, j := range jids {
			if j.Server != types.HiddenUserServer {
				continue
			}
			if pn, err := s.client.Store.LIDs.GetPNForLID(context.Background(), j); err == nil && pn.User != "" {
				if ci, err := s.client.Store.Contacts.GetContact(context.Background(), pn); err == nil && ci.Found {
					name = firstNonEmptyOf(ci.FullName, ci.PushName, ci.FirstName, ci.BusinessName)
					if push == "" {
						push = ci.PushName
					}
				}
			}
		}
	}
	return name, push
}

// groupJSON serializa um GroupInfo para a resposta da API.
func (s *Session) groupJSON(g *types.GroupInfo) map[string]any {
	parts := make([]map[string]any, 0, len(g.Participants))
	for _, p := range g.Participants {
		parts = append(parts, s.participantJSON(p))
	}
	return map[string]any{
		"jid":          g.JID.String(),
		"name":         g.Name,
		"topic":        g.Topic,
		"owner":        g.OwnerJID.String(),
		"announce":     g.IsAnnounce, // só admins enviam
		"locked":       g.IsLocked,   // só admins editam info
		"created":      g.GroupCreated.UnixMilli(),
		"participants": parts,
		// aliases WAHA
		"id":          g.JID.String(), // grupos já são @g.us
		"subject":     g.Name,
		"description": g.Topic,
	}
}

// group carrega a sessão pareada + o JID do grupo do path, ou escreve o erro.
func (s *server) group(w http.ResponseWriter, r *http.Request) (*Session, types.JID, bool) {
	sess := s.pairedSession(w, r.PathValue("sid"))
	if sess == nil {
		return nil, types.JID{}, false
	}
	gid, err := resolveGroupJID(r.PathValue("gid"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return nil, types.JID{}, false
	}
	return sess, gid, true
}

// POST /api/sessions/{sid}/groups  {name, participants:[...]}
func (s *server) handleCreateGroup(w http.ResponseWriter, r *http.Request) {
	sess := s.pairedSession(w, r.PathValue("sid"))
	if sess == nil {
		return
	}
	var b struct {
		Name         string   `json:"name"`
		Participants []string `json:"participants"`
	}
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil || strings.TrimSpace(b.Name) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name required"})
		return
	}
	parts, err := resolveParticipants(b.Participants)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	g, err := sess.client.CreateGroup(r.Context(), whatsmeow.ReqCreateGroup{Name: b.Name, Participants: parts})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, sess.groupJSON(g))
}

// GET /api/sessions/{sid}/groups  → grupos em que a conta está
func (s *server) handleListGroups(w http.ResponseWriter, r *http.Request) {
	sess := s.pairedSession(w, r.PathValue("sid"))
	if sess == nil {
		return
	}
	groups, err := sess.client.GetJoinedGroups(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	out := make([]map[string]any, 0, len(groups))
	for _, g := range groups {
		out = append(out, sess.groupJSON(g))
	}
	writeJSON(w, http.StatusOK, out)
}

// GET /api/sessions/{sid}/groups/{gid}
func (s *server) handleGroupInfo(w http.ResponseWriter, r *http.Request) {
	sess, gid, ok := s.group(w, r)
	if !ok {
		return
	}
	g, err := sess.client.GetGroupInfo(r.Context(), gid)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, sess.groupJSON(g))
}

// GET /api/sessions/{sid}/groups/{gid}/participants
func (s *server) handleGroupParticipants(w http.ResponseWriter, r *http.Request) {
	sess, gid, ok := s.group(w, r)
	if !ok {
		return
	}
	g, err := sess.client.GetGroupInfo(r.Context(), gid)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, sess.groupJSON(g)["participants"])
}

// POST /api/sessions/{sid}/groups/{gid}/participants/{action}
// action ∈ add | remove | promote | demote
func (s *server) handleGroupParticipantChange(action whatsmeow.ParticipantChange) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sess, gid, ok := s.group(w, r)
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
		res, err := sess.client.UpdateGroupParticipants(r.Context(), gid, parts, action)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		out := make([]map[string]any, 0, len(res))
		for _, p := range res {
			out = append(out, map[string]any{"jid": p.JID.String(), "isAdmin": p.IsAdmin})
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "participants": out})
	}
}

// PUT /api/sessions/{sid}/groups/{gid}/subject  {subject}
func (s *server) handleGroupSubject(w http.ResponseWriter, r *http.Request) {
	sess, gid, ok := s.group(w, r)
	if !ok {
		return
	}
	var b struct {
		Subject string `json:"subject"`
	}
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "subject required"})
		return
	}
	if err := sess.client.SetGroupName(r.Context(), gid, b.Subject); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// PUT /api/sessions/{sid}/groups/{gid}/description  {description}
func (s *server) handleGroupDescription(w http.ResponseWriter, r *http.Request) {
	sess, gid, ok := s.group(w, r)
	if !ok {
		return
	}
	var b struct {
		Description string `json:"description"`
	}
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "description required"})
		return
	}
	if err := sess.client.SetGroupTopic(r.Context(), gid, "", "", b.Description); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// PUT /api/sessions/{sid}/groups/{gid}/picture  {base64|url}
func (s *server) handleGroupPicture(w http.ResponseWriter, r *http.Request) {
	sess, gid, ok := s.group(w, r)
	if !ok {
		return
	}
	var b struct {
		Base64, URL string
	}
	_ = json.NewDecoder(r.Body).Decode(&b)
	data, err := fetchMedia(b.Base64, b.URL)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	pid, err := sess.client.SetGroupPhoto(r.Context(), gid, data)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "pictureId": pid})
}

// POST /api/sessions/{sid}/groups/{gid}/leave
func (s *server) handleLeaveGroup(w http.ResponseWriter, r *http.Request) {
	sess, gid, ok := s.group(w, r)
	if !ok {
		return
	}
	if err := sess.client.LeaveGroup(r.Context(), gid); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// GET /api/sessions/{sid}/groups/{gid}/invite  → link de convite
func (s *server) handleGroupInvite(w http.ResponseWriter, r *http.Request) {
	sess, gid, ok := s.group(w, r)
	if !ok {
		return
	}
	link, err := sess.client.GetGroupInviteLink(r.Context(), gid, false)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"link": link})
}

// POST /api/sessions/{sid}/groups/{gid}/invite/revoke  → revoga e gera novo link
func (s *server) handleGroupInviteRevoke(w http.ResponseWriter, r *http.Request) {
	sess, gid, ok := s.group(w, r)
	if !ok {
		return
	}
	link, err := sess.client.GetGroupInviteLink(r.Context(), gid, true)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"link": link})
}

// POST /api/sessions/{sid}/groups/join  {code}  (code = link completo ou só o código)
func (s *server) handleJoinGroup(w http.ResponseWriter, r *http.Request) {
	sess := s.pairedSession(w, r.PathValue("sid"))
	if sess == nil {
		return
	}
	var b struct {
		Code string `json:"code"`
	}
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil || strings.TrimSpace(b.Code) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "code required"})
		return
	}
	gid, err := sess.client.JoinGroupWithLink(r.Context(), inviteCode(b.Code))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"jid": gid.String()})
}

// GET /api/sessions/{sid}/groups/join-info?code=...  → info do grupo antes de entrar
func (s *server) handleGroupJoinInfo(w http.ResponseWriter, r *http.Request) {
	sess := s.pairedSession(w, r.PathValue("sid"))
	if sess == nil {
		return
	}
	code := strings.TrimSpace(r.URL.Query().Get("code"))
	if code == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "code required"})
		return
	}
	g, err := sess.client.GetGroupInfoFromLink(r.Context(), inviteCode(code))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, sess.groupJSON(g))
}

// PUT /api/sessions/{sid}/groups/{gid}/settings/announce  {enabled}
// enabled=true → só admins enviam mensagens.
func (s *server) handleGroupAnnounce(w http.ResponseWriter, r *http.Request) {
	sess, gid, ok := s.group(w, r)
	if !ok {
		return
	}
	var b struct {
		Enabled bool `json:"enabled"`
	}
	_ = json.NewDecoder(r.Body).Decode(&b)
	if err := sess.client.SetGroupAnnounce(r.Context(), gid, b.Enabled); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// PUT /api/sessions/{sid}/groups/{gid}/settings/locked  {enabled}
// enabled=true → só admins editam as infos do grupo.
func (s *server) handleGroupLocked(w http.ResponseWriter, r *http.Request) {
	sess, gid, ok := s.group(w, r)
	if !ok {
		return
	}
	var b struct {
		Enabled bool `json:"enabled"`
	}
	_ = json.NewDecoder(r.Body).Decode(&b)
	if err := sess.client.SetGroupLocked(r.Context(), gid, b.Enabled); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// inviteCode extrai o código puro de um link chat.whatsapp.com/XXsss ou devolve como veio.
func inviteCode(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.LastIndex(s, "/"); i >= 0 {
		s = s[i+1:]
	}
	return s
}

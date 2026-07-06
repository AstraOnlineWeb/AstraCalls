package main

import (
	"encoding/json"
	"net/http"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"
)

// GET /api/sessions/{sid}/groups/{gid}/requests  → fila de solicitações de entrada
func (s *server) handleGroupRequests(w http.ResponseWriter, r *http.Request) {
	sess, gid, ok := s.group(w, r)
	if !ok {
		return
	}
	reqs, err := sess.client.GetGroupRequestParticipants(r.Context(), gid)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	out := make([]map[string]any, 0, len(reqs))
	for _, req := range reqs {
		out = append(out, map[string]any{
			"jid":         req.JID.String(),
			"requestedAt": req.RequestedAt.Unix(),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"requests": out})
}

// handleGroupRequestChange aprova/recusa solicitações de entrada.
// POST /api/sessions/{sid}/groups/{gid}/requests/approve|reject  {participants}
func (s *server) handleGroupRequestChange(action whatsmeow.ParticipantRequestChange) http.HandlerFunc {
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
		res, err := sess.client.UpdateGroupRequestParticipants(r.Context(), gid, parts, action)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		out := make([]map[string]any, 0, len(res))
		for _, p := range res {
			out = append(out, map[string]any{"jid": p.JID.String(), "error": p.Error})
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "participants": out})
	}
}

// PUT /api/sessions/{sid}/groups/{gid}/settings/approval  {enabled}
// exige aprovação de admin para novos membros entrarem.
func (s *server) handleGroupApprovalMode(w http.ResponseWriter, r *http.Request) {
	sess, gid, ok := s.group(w, r)
	if !ok {
		return
	}
	var b struct {
		Enabled bool `json:"enabled"`
	}
	_ = json.NewDecoder(r.Body).Decode(&b)
	if err := sess.client.SetGroupJoinApprovalMode(r.Context(), gid, b.Enabled); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "enabled": b.Enabled})
}

// PUT /api/sessions/{sid}/groups/{gid}/settings/add-mode  {mode}
// mode: "admin_add" (só admins adicionam) | "all_member_add" (todos adicionam)
func (s *server) handleGroupAddMode(w http.ResponseWriter, r *http.Request) {
	sess, gid, ok := s.group(w, r)
	if !ok {
		return
	}
	var b struct {
		Mode string `json:"mode"`
	}
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil || b.Mode == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "mode required (admin_add|all_member_add)"})
		return
	}
	if err := sess.client.SetGroupMemberAddMode(r.Context(), gid, types.GroupMemberAddMode(b.Mode)); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "mode": b.Mode})
}

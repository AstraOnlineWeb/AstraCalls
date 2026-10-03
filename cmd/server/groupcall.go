package main

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strings"

	"wacalls/internal/voip/call"
	"wacalls/internal/wa"

	waBinary "go.mau.fi/whatsmeow/binary"
	"go.mau.fi/whatsmeow/types"
)

// Chamada em GRUPO — EXPERIMENTAL, atrás da flag WACALLS_GROUP_CALLS. O control
// plane (offer, roster, troca da chave de epoch) e a cripto estão implementados e a
// derivação é validada byte-a-byte; o acoplamento com o relay de grupo e a aceitação
// do offer pelo servidor só se validam em chamada real. Ver
// internal/voip/call/groupmix/README.md.

func groupCallsEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("WACALLS_GROUP_CALLS"))) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// ensureGroupCall cria (sob demanda) o gerenciador de chamada em grupo da sessão.
func (s *Session) ensureGroupCall() *call.GroupCallManager {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.groupCall == nil {
		gc := call.NewGroupCallManager(wa.NewSocket(s.client), s.log)
		gc.OnEnded = func(callID string) { s.log.Info("group call ended", "call_id", callID) }
		// TODO(live): ligar gc.OnPeerAudio ao bridge do navegador e alimentar
		// gc.FeedCapturedPCM com o áudio do atendente (como no 1:1). Depende da
		// validação do media plane em chamada real.
		s.groupCall = gc
	}
	return s.groupCall
}

// routeGroupUnknownCall encaminha um nó de controle de chamada para o grupo quando a
// flag está ligada e o nó pertence ao grupo ativo. Devolve true se tratou.
func (s *Session) routeGroupUnknownCall(ctx context.Context, node *waBinary.Node) bool {
	if !groupCallsEnabled() {
		return false
	}
	s.mu.Lock()
	gc := s.groupCall
	s.mu.Unlock()
	if gc == nil || gc.CallID() == "" {
		return false
	}
	if callIDFromNode(node) != gc.CallID() {
		return false
	}
	gc.HandleUnknownCall(ctx, node)
	return true
}

// handleStartGroupCall inicia uma chamada em grupo ad-hoc (POST /calls/group).
func (s *server) handleStartGroupCall(w http.ResponseWriter, r *http.Request) {
	if !groupCallsEnabled() {
		writeJSON(w, http.StatusNotImplemented, map[string]string{
			"error": "chamada em grupo desligada (experimental): ligue WACALLS_GROUP_CALLS=1",
		})
		return
	}
	sess := s.pairedSession(w, r.PathValue("sid"))
	if sess == nil {
		return
	}
	var body struct {
		Numbers  []string `json:"numbers"`
		GroupJid string   `json:"groupJid"`
		Video    bool     `json:"video"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)

	var targets []types.JID
	var groupJID types.JID

	if g := strings.TrimSpace(body.GroupJid); g != "" {
		// Modo "ligar pra um grupo que EXISTE": resolve os membros do grupo (exceto nós).
		jid, err := types.ParseJID(g)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "groupJid inválido"})
			return
		}
		groupJID = jid
		info, err := sess.client.GetGroupInfo(r.Context(), jid)
		if err != nil || info == nil {
			msg := "grupo não encontrado"
			if err != nil {
				msg = err.Error()
			}
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": "não consegui ler o grupo: " + msg})
			return
		}
		ownLID := sess.client.Store.GetLID().ToNonAD()
		var ownPN types.JID
		if id := sess.client.Store.ID; id != nil {
			ownPN = id.ToNonAD()
		}
		for _, p := range info.Participants {
			t := p.JID.ToNonAD()
			if t.IsEmpty() || t == ownLID || t == ownPN {
				continue
			}
			targets = append(targets, t)
		}
	} else {
		// Modo ad-hoc: lista de números.
		for _, n := range body.Numbers {
			if p := normalizePhone(n); p != "" {
				targets = append(targets, types.NewJID(p, types.DefaultUserServer))
			}
		}
	}

	if len(targets) < 2 {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "chamada em grupo precisa de 2+ destinos (grupo com você + 2 membros, ou numbers com 2+)",
		})
		return
	}
	if len(targets) > 31 {
		targets = targets[:31] // limite do protocolo
	}

	gc := sess.ensureGroupCall()
	callID, err := gc.StartGroupCall(r.Context(), targets, groupJID, body.Video)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"callId": callID, "targets": len(targets), "video": body.Video})
}

// handleEndGroupCall encerra a chamada em grupo ativa (POST /calls/group/{id}/end).
func (s *server) handleEndGroupCall(w http.ResponseWriter, r *http.Request) {
	if !groupCallsEnabled() {
		writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "chamada em grupo desligada"})
		return
	}
	sess := s.sessionByID(w, r.PathValue("sid"))
	if sess == nil {
		return
	}
	sess.mu.Lock()
	gc := sess.groupCall
	sess.mu.Unlock()
	if gc == nil || gc.CallID() == "" {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "nenhuma chamada em grupo ativa"})
		return
	}
	gc.End()
	writeJSON(w, http.StatusOK, map[string]string{"status": "ended"})
}

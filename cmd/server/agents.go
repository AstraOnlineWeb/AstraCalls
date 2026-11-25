package main

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"
)

// Roster de atendentes conectados (para transferir a um colega específico).
// Cada navegador conectado no SSE /api/events é um "cliente" (clientId); o nome
// vem de ?label= no SSE ou de POST /api/calls/register {label}.

type agentInfo struct {
	ClientID    string `json:"clientId"`
	Label       string `json:"label"`
	Busy        bool   `json:"busy"`
	CallID      string `json:"callId,omitempty"`
	Connections int    `json:"connections"`
	ConnectedAt int64  `json:"connectedAt"`
}

// labelOfLocked devolve o nome já registrado para o clientId (b.mu travado).
func (b *Broker) labelOfLocked(clientID string) string {
	if clientID == "" {
		return ""
	}
	for s := range b.subs {
		if s.clientID == clientID && s.label != "" {
			return s.label
		}
	}
	return ""
}

// labelOf devolve o nome do atendente (clientId) ou "".
func (b *Broker) labelOf(clientID string) string {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.labelOfLocked(clientID)
}

// setLabel grava o nome em todas as conexões do clientId. false = não conectado.
func (b *Broker) setLabel(clientID, label string) bool {
	b.mu.Lock()
	found := false
	for s := range b.subs {
		if s.clientID == clientID {
			s.label = label
			found = true
		}
	}
	b.mu.Unlock()
	if found {
		b.emitAgentsChanged()
	}
	return found
}

// isConnected diz se há conexão SSE do clientId que enxerga a sessão.
func (b *Broker) isConnected(sessionID, clientID string) bool {
	sc := b.scopeOf(sessionID)
	b.mu.RLock()
	defer b.mu.RUnlock()
	for s := range b.subs {
		if s.clientID == clientID && s.wants(sc) {
			return true
		}
	}
	return false
}

// agentsFor lista os atendentes conectados que enxergam a sessão.
func (b *Broker) agentsFor(sessionID string) []agentInfo {
	sc := b.scopeOf(sessionID)
	byID := map[string]*agentInfo{}
	b.mu.RLock()
	for s := range b.subs {
		if s.clientID == "" || !s.wants(sc) {
			continue
		}
		a := byID[s.clientID]
		if a == nil {
			a = &agentInfo{ClientID: s.clientID, ConnectedAt: s.connectedAt}
			byID[s.clientID] = a
		}
		a.Connections++
		if a.Label == "" {
			a.Label = s.label
		}
		if s.connectedAt != 0 && (a.ConnectedAt == 0 || s.connectedAt < a.ConnectedAt) {
			a.ConnectedAt = s.connectedAt
		}
	}
	b.mu.RUnlock()
	out := make([]agentInfo, 0, len(byID))
	for _, a := range byID {
		if id := b.ownerActiveCall(a.ClientID); id != "" {
			a.Busy, a.CallID = true, id
		}
		out = append(out, *a)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Label != out[j].Label {
			return strings.ToLower(out[i].Label) < strings.ToLower(out[j].Label)
		}
		return out[i].ClientID < out[j].ClientID
	})
	return out
}

// emitAgentsChanged avisa os painéis para recarregarem o roster (evento leve,
// sem a lista: cada painel busca a da sua sessão em /calls/agents).
func (b *Broker) emitAgentsChanged() {
	b.broadcast(map[string]any{"type": "agents-changed"})
}

// GET /api/sessions/{sid}/calls/agents (alias /calls/clients)
func (s *server) handleCallAgents(w http.ResponseWriter, r *http.Request) {
	sess := s.sessionByID(w, r.PathValue("sid"))
	if sess == nil {
		return
	}
	me := clientID(r)
	list := s.broker.agentsFor(sess.id)
	if r.URL.Query().Get("includeSelf") != "true" && me != "" {
		kept := list[:0]
		for _, a := range list {
			if a.ClientID != me {
				kept = append(kept, a)
			}
		}
		list = kept
	}
	writeJSON(w, http.StatusOK, map[string]any{"agents": list})
}

// POST /api/calls/register {label} — nomeia o clientId (header X-Client-Id ou
// ?clientId=) em todas as conexões SSE dele.
func (s *server) handleCallRegister(w http.ResponseWriter, r *http.Request) {
	cid := clientID(r)
	var b struct {
		Label string `json:"label"`
	}
	_ = json.NewDecoder(r.Body).Decode(&b)
	if cid == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "clientId obrigatório (header X-Client-Id ou ?clientId=)"})
		return
	}
	label := strings.TrimSpace(b.Label)
	if !s.broker.setLabel(cid, label) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "clientId sem conexão no SSE /api/events"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"clientId": cid, "label": label})
}

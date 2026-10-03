package main

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"wacalls/internal/voip/call"
	"wacalls/internal/wa"

	"github.com/coder/websocket"
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
	// Deixa o manager parsear o envelope e decidir se é do grupo ativo (o callID de
	// eventos de controle de grupo nem sempre vem no formato do callIDFromNode).
	// Só consideramos "tratado" (true) quando casa o callID; senão deixa seguir o 1:1.
	cid := callIDFromNode(node)
	if cid != "" && cid != gc.CallID() {
		return false
	}
	// Handling ASSÍNCRONO: ConnectGroupRelay (handshake DTLS) e Query de rekey podem
	// DEMORAR/TRAVAR; rodar no handler síncrono do whatsmeow congela TODO o
	// processamento de nós da sessão (o <video state> seguinte ficava "Node handling
	// taking long" por minutos e a call nunca conectava). Rodamos em goroutine própria.
	go gc.HandleUnknownCall(context.Background(), node)
	return cid == gc.CallID()
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

// handleGroupWSBridge atende GET /api/sessions/{sid}/calls/group/ws — bridge de áudio
// PCM16 entre o navegador do operador e a chamada em grupo (ouvir o mix + falar).
func (s *server) handleGroupWSBridge(w http.ResponseWriter, r *http.Request) {
	if !groupCallsEnabled() {
		writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "grupo desligado"})
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

	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		Subprotocols:       []string{"pcm16"},
		InsecureSkipVerify: true,
	})
	if err != nil {
		s.log.Warn("group ws_bridge: upgrade failed", "err", err)
		return
	}
	events := r.URL.Query().Get("events") == "1" || r.URL.Query().Get("events") == "true"
	bridge := newWSBridge(conn, s.log, events)
	bridge.OnBrowserPCM = func(pcm16 []float32) { gc.FeedCapturedPCM(pcm16) }
	gc.SetAudioSink(func(pcm16 []float32) { _ = bridge.WritePCM(pcm16) })
	s.log.Info("group ws_bridge: connected", "sid", sess.id, "call", gc.CallID())

	go bridge.keepAlive()
	bridge.readLoop()

	gc.SetAudioSink(nil)
	bridge.Close()
	s.log.Info("group ws_bridge: disconnected", "sid", sess.id)
}

// handleEndGroupCall encerra a chamada em grupo ativa (POST /calls/group/end).
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

// handleGroupVideoWS é o transporte de VÍDEO (H264) da chamada em grupo entre o
// navegador e o gateway. Frames binários:
//   - gateway → navegador: [1 byte len(pid)][pid ascii][access unit H264 Annex-B]
//     (o navegador decodifica/renderiza por participante).
//   - navegador → gateway: access unit H264 Annex-B puro (câmera do atendente) →
//     FeedCapturedVideo.
//
// É um WS separado do de áudio (que é PCM16) p/ não misturar os formatos.
func (s *server) handleGroupVideoWS(w http.ResponseWriter, r *http.Request) {
	if !groupCallsEnabled() {
		writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "grupo desligado"})
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

	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		Subprotocols:       []string{"h264-group"},
		InsecureSkipVerify: true,
	})
	if err != nil {
		s.log.Warn("group video_ws: upgrade failed", "err", err)
		return
	}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	// gateway → navegador: serializa writes (a lib não aceita writes concorrentes).
	var wmu sync.Mutex
	gc.SetVideoSink(func(pid string, au []byte) {
		if len(au) == 0 || len(pid) > 255 {
			return
		}
		msg := make([]byte, 0, 1+len(pid)+len(au))
		msg = append(msg, byte(len(pid)))
		msg = append(msg, pid...)
		msg = append(msg, au...)
		wmu.Lock()
		defer wmu.Unlock()
		wctx, c := context.WithTimeout(ctx, 2*time.Second)
		_ = conn.Write(wctx, websocket.MessageBinary, msg)
		c()
	})
	s.log.Info("group video_ws: connected", "sid", sess.id, "call", gc.CallID())

	// navegador → gateway: cada frame binário é um AU H264 da câmera do atendente.
	for {
		typ, data, rerr := conn.Read(ctx)
		if rerr != nil {
			break
		}
		if typ == websocket.MessageBinary && len(data) > 0 {
			gc.FeedCapturedVideo(data)
		}
	}

	gc.SetVideoSink(nil)
	_ = conn.Close(websocket.StatusNormalClosure, "")
	s.log.Info("group video_ws: disconnected", "sid", sess.id)
}

package main

import (
	"context"
	"log/slog"
	"os"
	"sync"
	"time"

	"wacalls/internal/voip/call"
	"wacalls/internal/voip/core"
	"wacalls/internal/voip/media"
	"wacalls/internal/voip/signaling"
	"wacalls/internal/voip/wanode"
	"wacalls/internal/wa"

	"database/sql"

	"github.com/mdp/qrterminal/v3"
	"go.mau.fi/whatsmeow"
	waBinary "go.mau.fi/whatsmeow/binary"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

type Session struct {
	id   string
	name string
	mgr  *SessionManager
	log  *slog.Logger

	client *whatsmeow.Client
	reg    *callRegistry

	// store próprio desta sessão (1 banco por sessão)
	waContainer *sqlstore.Container
	waDB        *sql.DB

	mu        sync.Mutex
	auth      AuthSnapshot
	webhook   string
	chatwoot  ChatwootConfig
	recording bool   // grava as chamadas desta sessão (opt-in)
	proxy     string // proxy de saída da conexão WhatsApp (http/https/socks5)

	// sentIDs guarda os IDs de mensagens que ESTE cliente enviou (via API ou
	// pelo agente do Chatwoot), para não espelhá-las como nota privada quando
	// voltarem como evento from_me. msgID -> unixMilli.
	sentIDs sync.Map
}

// markSelfSent registra uma mensagem enviada por nós (com prune do que é antigo).
func (s *Session) markSelfSent(id string) {
	if id == "" {
		return
	}
	now := time.Now().UnixMilli()
	s.sentIDs.Store(id, now)
	s.sentIDs.Range(func(k, v any) bool {
		if ts, ok := v.(int64); ok && now-ts > 10*60*1000 {
			s.sentIDs.Delete(k)
		}
		return true
	})
}

// isSelfSent diz se a mensagem foi enviada por nós (API/agente), não pelo aparelho.
func (s *Session) isSelfSent(id string) bool {
	_, ok := s.sentIDs.Load(id)
	return ok
}

// sendAndMark envia uma mensagem e a registra como "enviada por nós".
func (s *Session) sendAndMark(ctx context.Context, jid types.JID, msg *waE2E.Message) error {
	resp, err := s.client.SendMessage(ctx, jid, msg)
	if err == nil {
		s.markSelfSent(resp.ID)
	}
	return err
}

func (s *Session) setWebhook(url string) {
	s.mu.Lock()
	s.webhook = url
	s.mu.Unlock()
}

func (s *Session) getWebhook() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.webhook
}

func (s *Session) setChatwoot(c ChatwootConfig) {
	s.mu.Lock()
	s.chatwoot = c
	s.mu.Unlock()
}

func (s *Session) getChatwoot() ChatwootConfig {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.chatwoot
}

func (s *Session) setRecording(on bool) {
	s.mu.Lock()
	s.recording = on
	s.mu.Unlock()
}

func (s *Session) getRecording() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.recording
}

func (s *Session) setProxy(url string) {
	s.mu.Lock()
	s.proxy = url
	s.mu.Unlock()
}

func (s *Session) getProxy() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.proxy
}

// applyProxy aplica o proxy configurado ao client whatsmeow. Precisa rodar ANTES
// do Connect(); trocar depois exige reconnect() (o whatsmeow só relê no dial).
func (s *Session) applyProxy() {
	addr := s.getProxy()
	if err := s.client.SetProxyAddress(addr); err != nil {
		s.log.Warn("proxy inválido, conectando sem proxy", "err", err)
	}
}

// reconnect derruba e reconecta a sessão pareada para o novo proxy valer.
func (s *Session) reconnect() {
	if s.client.Store.ID == nil {
		return // não pareada: o proxy será aplicado no próximo pareamento
	}
	s.client.Disconnect()
	s.applyProxy()
	if err := s.client.Connect(); err != nil {
		s.log.Error("reconexão após troca de proxy falhou", "err", err)
	}
}

func newSession(mgr *SessionManager, id, name string, client *whatsmeow.Client) *Session {
	s := &Session{
		id:     id,
		name:   name,
		mgr:    mgr,
		log:    mgr.log.With("session", id),
		client: client,
		auth:   AuthSnapshot{State: "connecting"},
		reg:    newCallRegistry(),
	}
	client.AddEventHandler(s.handleEvent)
	return s
}

func (s *Session) createCall(callID string) *call.CallManager {
	cm := call.NewCallManager(wa.NewSocket(s.client), s.log)
	s.wireCall(cm, callID)
	ac := &activeCall{cm: cm}
	if s.getRecording() {
		ac.recorder = newCallRecorder(callID, s.log, time.Now())
	}
	s.reg.add(callID, ac)
	return cm
}

func (s *Session) wireCall(cm *call.CallManager, callID string) {
	cm.OnIncoming = func(c *call.CallInfo) {
		s.mgr.broker.upsertCall(CallRecord{
			SessionID: s.id, CallID: c.CallID, Direction: "inbound", Peer: c.PeerJid,
			StartedAt: time.Now().UnixMilli(), Status: StatusRinging,
		})
		s.mgr.broker.emitIncoming(s.id, c.CallID, c.PeerJid)
	}
	cm.OnStateChange = func(c *call.CallInfo) {
		if c.IsEnded() {
			s.removeCall(c.CallID)
			s.mgr.broker.endCall(c.CallID, string(c.StateData.EndReason))
			return
		}
		dir := "outbound"
		if c.Direction == core.CallDirectionIncoming {
			dir = "inbound"
		}
		existing, _ := s.mgr.broker.getCall(c.CallID)
		rec := CallRecord{
			SessionID: s.id, CallID: c.CallID, Direction: dir, Peer: c.PeerJid,
			StartedAt: time.Now().UnixMilli(), Status: mapStatus(c.StateData.State),
		}
		if existing != nil {
			rec.Owner = existing.Owner
			rec.StartedAt = existing.StartedAt
		}
		s.mgr.broker.upsertCall(rec)
	}
	cm.OnEnded = func(c *call.CallInfo) {
		s.removeCall(c.CallID)
		s.mgr.broker.endCall(c.CallID, string(c.StateData.EndReason))
	}
	cm.OnPeerAudio = func(pcm16 []float32) {
		ac, ok := s.reg.get(callID)
		if !ok {
			return
		}
		// grava o lado do peer (WhatsApp) mesmo se o navegador ainda não estiver pronto
		ac.recorder.writePeer(pcm16)
		if ac.bridge == nil || ac.browserOpus == nil {
			return
		}
		pcm48 := media.Upsample16to48(pcm16)
		opus, err := ac.browserOpus.Encode(pcm48)
		if err != nil || len(opus) == 0 {
			return
		}
		_ = ac.bridge.WriteOpus(opus, 60*time.Millisecond)
	}
}

func (s *Session) startOutgoing(ctx context.Context, peer types.JID, isVideo bool) (string, error) {
	callID := signaling.GenerateCallID()
	cm := s.createCall(callID)
	if err := cm.StartCall(ctx, callID, peer, isVideo); err != nil {
		s.removeCall(callID)
		return "", err
	}
	return callID, nil
}

func (s *Session) callForEvent(from types.JID, data *waBinary.Node) (*activeCall, bool) {
	callID := callIDFromNode(wrapCall(from, data))
	if callID == "" {
		return nil, false
	}
	return s.reg.get(callID)
}

func (s *Session) onIncomingOffer(ctx context.Context, evt *events.CallOffer) {
	node := wrapCall(evt.From, evt.Data)
	callID := callIDFromNode(node)
	if callID == "" {
		return
	}
	if max := s.mgr.maxCalls; max > 0 && s.reg.count() >= max {
		s.rejectOffer(ctx, node, evt.From)
		return
	}
	cm := s.createCall(callID)
	cm.HandleCallOffer(ctx, node, evt.From)
}

func (s *Session) rejectOffer(ctx context.Context, node *waBinary.Node, from types.JID) {
	info := signaling.ExtractNodeInfo(node)
	if info == nil {
		return
	}
	creator := wanode.AttrString(info.InnerNode.Attrs, "call-creator")
	if creator == "" {
		creator = from.String()
	}
	reject := signaling.BuildRejectStanza(from, info.CallID, wanode.MustJID(creator))
	_ = wa.NewSocket(s.client).SendNode(ctx, reject)
	s.log.Info("inbound call rejected: session at capacity", "call_id", info.CallID)
}

func (s *Session) handleEvent(rawEvt any) {
	ctx := context.Background()
	switch evt := rawEvt.(type) {
	case *events.Connected:
		if id := s.client.Store.ID; id != nil {
			_ = s.mgr.store.setJID(s.mgr.appCtx, s.id, id.String())
		}
		s.setAuth(AuthSnapshot{State: "open", Paired: true})
	case *events.LoggedOut:
		s.setAuth(AuthSnapshot{State: "logged_out", Paired: false})
	case *events.Message:
		if evt.Message.GetPollUpdateMessage() != nil {
			go s.handleIncomingPollVote(evt) // voto em enquete (decodifica + encaminha)
		} else {
			s.storeMessageEvent(evt)
			s.dispatchWebhook("message", summarizeMessage(evt))
			go s.chatwootPushIncoming(evt)
		}
	case *events.Receipt:
		s.dispatchWebhook("receipt", map[string]any{
			"chat": evt.Chat.String(), "sender": evt.Sender.String(),
			"type": string(evt.Type), "ids": evt.MessageIDs,
			"timestamp": evt.Timestamp.UnixMilli(),
		})
	case *events.CallOffer:
		s.onIncomingOffer(ctx, evt)
	case *events.CallAccept:
		if ac, ok := s.callForEvent(evt.From, evt.Data); ok {
			ac.cm.HandleCallAccept(ctx, wrapCall(evt.From, evt.Data), evt.From)
		}
	case *events.CallTransport:
		if ac, ok := s.callForEvent(evt.From, evt.Data); ok {
			ac.cm.HandleCallTransport(ctx, wrapCall(evt.From, evt.Data), evt.From)
		}
	case *events.CallTerminate:
		if ac, ok := s.callForEvent(evt.From, evt.Data); ok {
			ac.cm.HandleCallTerminate(wrapCall(evt.From, evt.Data))
		}
	case *events.CallReject:
		if ac, ok := s.callForEvent(evt.From, evt.Data); ok {
			ac.cm.HandleCallTerminate(wrapCall(evt.From, evt.Data))
		}
	}
}

func (s *Session) connect(ctx context.Context) error {
	s.applyProxy()
	if s.client.Store.ID != nil {
		return s.client.Connect()
	}
	return s.startPairing(ctx)
}

func (s *Session) startPairing(ctx context.Context) error {
	s.applyProxy()
	qrChan, err := s.client.GetQRChannel(ctx)
	if err != nil {
		return err
	}
	if err := s.client.Connect(); err != nil {
		return err
	}
	go func() {
		for evt := range qrChan {
			switch evt.Event {
			case "code":
				s.log.Info("scan the QR code to pair this session")
				qrterminal.GenerateHalfBlock(evt.Code, qrterminal.L, os.Stdout)
				s.setAuth(AuthSnapshot{State: "qr", QR: evt.Code})
				s.mgr.broker.emitSessionQR(s.id, evt.Code)
			case "success":
				if id := s.client.Store.ID; id != nil {
					_ = s.mgr.store.setJID(s.mgr.appCtx, s.id, id.String())
				}
				s.setAuth(AuthSnapshot{State: "open", Paired: true})
			case "timeout":
				s.setAuth(AuthSnapshot{State: "logged_out", Paired: false})
			}
		}
	}()
	return nil
}

func (s *Session) setAuth(a AuthSnapshot) {
	s.mu.Lock()
	s.auth = a
	s.mu.Unlock()
	s.mgr.broker.emitAuthState(s.id, a)
	s.mgr.broker.emitSessionList(s.mgr.infos())
}

func (s *Session) info() SessionInfo {
	s.mu.Lock()
	a := s.auth
	rec := s.recording
	s.mu.Unlock()
	jid := ""
	if id := s.client.Store.ID; id != nil {
		jid = id.String()
	}
	return SessionInfo{ID: s.id, Name: s.name, JID: jid, State: a.State, Paired: a.Paired || jid != "", Recording: rec}
}

func (s *Session) setBridge(callID string, b *Bridge, oc media.Codec) {
	oldB, oldOC, found := s.reg.setBridge(callID, b, oc)
	if !found {
		b.Close()
		if oc != nil {
			oc.Close()
		}
		return
	}
	if oldB != nil {
		oldB.Close()
	}
	if oldOC != nil {
		oldOC.Close()
	}
}

func (s *Session) removeCall(callID string) {
	ac, ok := s.reg.remove(callID)
	if !ok {
		return
	}
	s.finalizeRecording(ac)
	if ac.bridge != nil {
		ac.bridge.Close()
	}
	if ac.browserOpus != nil {
		ac.browserOpus.Close()
	}
}

// finalizeRecording encerra a gravação (encode MP3) e entrega o áudio (Chatwoot
// + webhook). Roda em goroutine pois o encode (ffmpeg) é lento e não pode segurar
// o teardown. finish() é idempotente, então é seguro chamar pelos dois caminhos
// de término (removeCall / teardownAllCalls).
func (s *Session) finalizeRecording(ac *activeCall) {
	if ac == nil || ac.recorder == nil {
		return
	}
	rec := ac.recorder
	callID := rec.callID
	peer := ""
	if cr, ok := s.mgr.broker.getCall(callID); ok && cr != nil {
		peer = cr.Peer
	}
	go func() {
		path, seconds, ok := rec.finish()
		if !ok {
			return
		}
		s.onRecordingReady(callID, peer, path, seconds)
	}()
}

func (s *Session) terminateCall(callID string, reason core.EndCallReason) {
	ac, ok := s.reg.get(callID)
	if !ok {
		return
	}
	_ = ac.cm.EndCall(context.Background(), reason)
}

func (s *Session) teardownAllCalls() {
	for _, ac := range s.reg.drain() {
		_ = ac.cm.EndCall(context.Background(), core.EndCallReasonUserEnded)
		s.finalizeRecording(ac)
		if ac.bridge != nil {
			ac.bridge.Close()
		}
		if ac.browserOpus != nil {
			ac.browserOpus.Close()
		}
	}
}

func (s *Session) replaceClient(client *whatsmeow.Client) {
	s.teardownAllCalls()
	s.client.Disconnect()
	s.client = client
	client.AddEventHandler(s.handleEvent)
}

func (s *Session) shutdown() {
	s.teardownAllCalls()
	s.client.Disconnect()
	if s.waDB != nil {
		_ = s.waDB.Close()
	}
}

func mapStatus(state core.CallState) CallStatus {
	switch state {
	case core.CallStateActive:
		return StatusConnected
	case core.CallStateEnded:
		return StatusEnded
	case core.CallStateInitiating:
		return StatusStarting
	default:
		return StatusRinging
	}
}

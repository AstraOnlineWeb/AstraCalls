package main

import (
	"context"
	"encoding/json"
	"errors"
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

	// downAlerted evita repetir o aviso de "sessão desconectada" no Chatwoot
	// enquanto ela segue caída; volta a false ao reconectar (events.Connected).
	downAlerted bool

	// sentIDs guarda os IDs de mensagens que ESTE cliente enviou, com a origem
	// (API ou agente do Chatwoot), para decidir o que fazer quando voltarem como
	// evento from_me. msgID -> selfSent.
	sentIDs sync.Map

	// importedMsgIDs deduplica a importação de histórico entre os chunks do
	// HistorySync (e reconexões enquanto o processo vive). msgID -> struct{}.
	importedMsgIDs sync.Map
	// importMu serializa a importação de histórico: os chunks do HistorySync
	// chegam em goroutines separadas e criariam contatos/conversas duplicados no
	// Chatwoot se rodassem ensureContact/ensureConversation concorrentemente.
	importMu sync.Mutex
}

// Origem de uma mensagem enviada por nós. O agente do Chatwoot nunca é espelhado
// (a mensagem já está na conversa); a API é espelhada quando o toggle mirror_api
// da sessão está ligado.
const (
	selfSentAPI      = "api"
	selfSentChatwoot = "chatwoot"
)

type selfSent struct {
	origin string
	ts     int64
}

// markSelfSent registra uma mensagem enviada por nós (com prune do que é antigo).
func (s *Session) markSelfSent(id, origin string) {
	if id == "" {
		return
	}
	now := time.Now().UnixMilli()
	s.sentIDs.Store(id, selfSent{origin: origin, ts: now})
	s.sentIDs.Range(func(k, v any) bool {
		if e, ok := v.(selfSent); ok && now-e.ts > 10*60*1000 {
			s.sentIDs.Delete(k)
		}
		return true
	})
}

// selfSentOrigin diz se a mensagem foi enviada por nós e por qual caminho
// (selfSentAPI/selfSentChatwoot). ok=false => veio do aparelho.
func (s *Session) selfSentOrigin(id string) (origin string, ok bool) {
	v, found := s.sentIDs.Load(id)
	if !found {
		return "", false
	}
	e, cast := v.(selfSent)
	if !cast {
		return "", false
	}
	return e.origin, true
}

// sendAndMark envia uma mensagem, a registra como "enviada por nós" e devolve o
// ID da mensagem do WhatsApp (usado p/ gravar o source_id no Chatwoot).
func (s *Session) sendAndMark(ctx context.Context, jid types.JID, msg *waE2E.Message) (string, error) {
	resp, err := s.client.SendMessage(ctx, jid, msg)
	if err != nil {
		return "", err
	}
	s.markSelfSent(resp.ID, selfSentChatwoot)
	return resp.ID, nil
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
	go s.runPresenceKeepalive()
	return s
}

// keepPresenceActive marca a conta como disponível para o WhatsApp registrar
// atividade do dispositivo vinculado e não removê-lo por inatividade. Ignora
// silenciosamente ErrNoPushName (o pushname ainda não chegou; reenviamos no
// evento PushNameSetting) e só loga outras falhas em debug.
func (s *Session) keepPresenceActive(ctx context.Context) {
	if err := s.client.SendPresence(ctx, types.PresenceAvailable); err != nil {
		if !errors.Is(err, whatsmeow.ErrNoPushName) {
			s.log.Debug("keepalive presence failed", "err", err)
		}
	}
}

// presenceKeepaliveInterval reforça a presença periodicamente: uma conexão
// estável pode ficar dias no ar sem reconectar, e a "última sessão ativa" só é
// atualizada quando enviamos presença — sem esse reforço a data envelhece e o
// WhatsApp acaba agendando a remoção do dispositivo por inatividade.
const presenceKeepaliveInterval = 6 * time.Hour

// runPresenceKeepalive vive junto com a sessão (encerra no shutdown do app) e
// reenvia a presença available enquanto a conta estiver conectada e pareada.
func (s *Session) runPresenceKeepalive() {
	ticker := time.NewTicker(presenceKeepaliveInterval)
	defer ticker.Stop()
	for {
		select {
		case <-s.mgr.appCtx.Done():
			return
		case <-ticker.C:
			if s.client.IsConnected() && s.client.IsLoggedIn() {
				s.keepPresenceActive(s.mgr.appCtx)
			}
		}
	}
}

// createCall monta a chamada. record liga a gravação nesta chamada específica —
// o chamador combina o opt-in por sessão (getRecording) com o flag por chamada.
func (s *Session) createCall(callID string, record bool) *call.CallManager {
	cm := call.NewCallManager(wa.NewSocket(s.client), s.log)
	s.wireCall(cm, callID)
	ac := &activeCall{cm: cm}
	if record {
		ac.recorder = newCallRecorder(callID, s.log, time.Now())
	}
	s.reg.add(callID, ac)
	return cm
}

func (s *Session) wireCall(cm *call.CallManager, callID string) {
	cm.OnIncoming = func(c *call.CallInfo) {
		// numa chamada o PeerJid quase sempre vem como LID (123@lid); resolve pro
		// telefone real (PN) e, se der, pro nome do contato — senão a UI/widget
		// mostra o LID cru (issue #9).
		phone, name := s.resolvePeer(c.PeerJid)
		s.mgr.broker.upsertCall(CallRecord{
			SessionID: s.id, CallID: c.CallID, Direction: "inbound", Peer: c.PeerJid,
			StartedAt: time.Now().UnixMilli(), Status: StatusRinging,
		})
		// diagnóstico: mostra o account_id da sessão e quantos assinantes vão
		// receber. subs_matched inclui o painel (accountID 0); se um widget está
		// aberto na conta certa, ele entra no matched — senão cai em
		// subs_widget_other_acct (conta divergente) ou nem aparece (não conectou).
		acct, total, matched, otherAcct := s.mgr.broker.subscriberScope(s.id)
		s.log.Info("incoming call: broadcasting", "callID", c.CallID, "peer", c.PeerJid,
			"acct", acct, "subs_total", total, "subs_matched", matched, "subs_widget_other_acct", otherAcct)
		s.mgr.broker.emitIncoming(s.id, c.CallID, c.PeerJid, phone, name, c.MediaType == core.CallMediaTypeVideo)
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
			Held: c.StateData.State == core.CallStateOnHold,
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
	cm.OnPeerVideo = func(au []byte) {
		ac, ok := s.reg.get(callID)
		if !ok || ac.bridge == nil {
			return
		}
		_ = ac.bridge.WriteVideo(au)
	}
	cm.OnVideoUpgradeRequest = func(c *call.CallInfo) {
		s.mgr.broker.emitVideoState(s.id, c.CallID, "upgrade-request", c.StateData.PeerVideoOn,
			!c.StateData.VideoOff, c.StateData.VideoUpgradeIncoming, c.StateData.VideoUpgradeOutgoing)
	}
	cm.OnVideoStateChanged = func(c *call.CallInfo) {
		s.mgr.broker.emitVideoState(s.id, c.CallID, "state", c.StateData.PeerVideoOn,
			!c.StateData.VideoOff, c.StateData.VideoUpgradeIncoming, c.StateData.VideoUpgradeOutgoing)
	}
}

func (s *Session) startOutgoing(ctx context.Context, peer types.JID, isVideo, record bool) (string, error) {
	callID := signaling.GenerateCallID()
	// grava se a sessão está em modo gravação OU se esta chamada pediu (flag record)
	cm := s.createCall(callID, record || s.getRecording())
	if err := cm.StartCall(ctx, callID, peer, isVideo); err != nil {
		s.removeCall(callID)
		return "", err
	}
	return callID, nil
}

// fakeCall dispara um toque fantasma (offer + terminate após dur) sem estabelecer
// mídia. Usa um CallManager transiente NÃO registrado no reg: é fire-and-forget e
// não deve ocupar o slot de chamada real da sessão.
func (s *Session) fakeCall(ctx context.Context, peer types.JID, isVideo bool, dur time.Duration) (string, error) {
	cm := call.NewCallManager(wa.NewSocket(s.client), s.log)
	return cm.FakeCall(ctx, peer, isVideo, dur)
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
	cm := s.createCall(callID, s.getRecording())
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

// maybeMarkRead confirma leitura automática das mensagens recebidas quando
// read_messages está ligado na config do Chatwoot (paridade com a Evolution).
func (s *Session) maybeMarkRead(ctx context.Context, evt *events.Message) {
	if !s.getChatwoot().ReadMessages || evt.Info.IsFromMe {
		return
	}
	// não faz sentido/formato p/ newsletters e status/broadcast
	if evt.Info.Chat.Server == types.NewsletterServer || evt.Info.Chat.Server == types.BroadcastServer {
		return
	}
	go func() {
		err := s.client.MarkRead(ctx, []types.MessageID{evt.Info.ID}, evt.Info.Timestamp, evt.Info.Chat, evt.Info.Sender)
		if err != nil {
			s.log.Debug("read_messages: mark read failed", "err", err)
		}
	}()
}

func (s *Session) handleEvent(rawEvt any) {
	ctx := context.Background()
	switch evt := rawEvt.(type) {
	case *events.Connected:
		if id := s.client.Store.ID; id != nil {
			_ = s.mgr.store.setJID(s.mgr.appCtx, s.id, id.String())
		}
		s.setAuth(AuthSnapshot{State: "open", Paired: true})
		// reconectou: rearma o aviso de queda para a próxima desconexão real.
		s.mu.Lock()
		s.downAlerted = false
		s.mu.Unlock()
		// Marca o dispositivo como ativo no WhatsApp (atualiza a "última sessão
		// ativa"). Sem enviar presença available, o WhatsApp trata o companion
		// como inativo e agenda a remoção por inatividade ("Será desconectado
		// hoje"), mesmo com o bridge conectado 24/7. No primeiro pareamento o
		// pushname pode não ter chegado ainda (SendPresence -> ErrNoPushName);
		// nesse caso o reenvio ocorre no evento PushNameSetting abaixo.
		go s.keepPresenceActive(ctx)
	case *events.PushNameSetting:
		// O pushname chegou (às vezes depois do Connected): reenvia a presença,
		// pois SendPresence falha enquanto o nome não está no store.
		go s.keepPresenceActive(ctx)
	case *events.LoggedOut:
		s.setAuth(AuthSnapshot{State: "logged_out", Paired: false})
		go s.notifyDisconnected("desconectada (o aparelho desvinculou este dispositivo)",
			"Reconecte lendo o QR no painel do AstraCalls para voltar a enviar e receber.")
	case *events.StreamReplaced:
		go s.notifyDisconnected("substituída por outra conexão do mesmo número",
			"Se não foi proposital, reconecte pelo painel do AstraCalls.")
	case *events.TemporaryBan:
		go s.notifyDisconnected("bloqueada temporariamente pelo WhatsApp ("+evt.String()+")",
			"Aguarde o fim do bloqueio e evite disparos em massa.")
	case *events.ClientOutdated:
		go s.notifyDisconnected("recusada pelo WhatsApp: cliente desatualizado",
			"É necessário atualizar o AstraCalls. Avise o suporte técnico.")
	case *events.Message:
		switch {
		case evt.Message.GetPollUpdateMessage() != nil:
			go s.handleIncomingPollVote(evt) // voto em enquete (decodifica + encaminha)
		case evt.Message.GetEncEventResponseMessage() != nil:
			go s.handleIncomingEventResponse(evt) // RSVP de evento (decodifica + encaminha)
		case evt.Message.GetReactionMessage() != nil || evt.Message.GetEncReactionMessage() != nil:
			go s.handleIncomingReaction(evt) // reação (emoji) numa mensagem
		default:
			s.storeMessageEvent(evt)
			s.dispatchWebhook("message", summarizeMessage(evt))
			go s.chatwootPushIncoming(evt)
			s.maybeMarkRead(ctx, evt)
		}
	case *events.HistorySync:
		// conversas antigas que o WhatsApp envia ao parear -> importa pro Chatwoot
		go s.importHistorySync(evt.Data)
	case *events.GroupInfo:
		// entrou/saiu/promoveu no grupo -> webhook + nota no Chatwoot
		go s.handleGroupSystemEvent(evt)
	case *events.UndecryptableMessage:
		// visualização única chega como placeholder "unavailable" — o WhatsApp não
		// libera o conteúdo p/ dispositivos vinculados. Avisa o atendente/webhook.
		if evt.IsUnavailable && evt.UnavailableType == events.UnavailableTypeViewOnce {
			go s.handleUnavailableViewOnce(evt)
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
	case *events.UnknownCallEvent:
		// stanzas de call sem evento tipado no whatsmeow — hoje o que nos interessa é
		// o <video state=N> de upgrade/downgrade mid-call.
		s.handleUnknownCall(ctx, evt)
	}
}

func (s *Session) handleUnknownCall(ctx context.Context, evt *events.UnknownCallEvent) {
	if evt.Node == nil {
		return
	}
	callID := callIDFromNode(evt.Node)
	if callID == "" {
		return
	}
	if ac, ok := s.reg.get(callID); ok {
		ac.cm.HandleVideoState(ctx, evt.Node)
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
			case "passkey-request":
				// conta com passkey: o WhatsApp exige uma prova WebAuthn do dono.
				// Expõe o desafio pro front (que delega ao autenticador via extensão)
				// e recebe a assinatura de volta em POST .../pair-passkey.
				s.setPasskeyChallenge(evt.PasskeyRequest.PublicKey)
			case "passkey-confirmation":
				// handoff manual: confirma o código exibido no WhatsApp do dono.
				if err := s.client.SendPasskeyConfirmation(s.mgr.appCtx); err != nil {
					s.log.Warn("passkey: confirmação falhou", "err", err)
				}
			case "error":
				s.log.Warn("pareamento: erro", "err", evt.Error)
			}
		}
	}()
	return nil
}

// setPasskeyChallenge serializa o desafio WebAuthn e o publica no estado de auth
// (via SSE), no mesmo modelo do QR. O front repassa esse objeto ao autenticador
// (navigator.credentials.get) na origem web.whatsapp.com através da extensão.
func (s *Session) setPasskeyChallenge(pk *types.WebAuthnPublicKey) {
	if pk == nil {
		return
	}
	raw, err := json.Marshal(pk)
	if err != nil {
		s.log.Warn("passkey: falha ao serializar desafio", "err", err)
		return
	}
	s.setAuth(AuthSnapshot{State: "passkey_request", Passkey: raw})
}

// startPhonePairing conecta um device novo e solicita um código de pareamento
// por telefone (o usuário digita o código no WhatsApp: Aparelhos conectados ->
// Conectar com número). O sucesso chega depois via events.Connected.
func (s *Session) startPhonePairing(ctx context.Context, phone string) (string, error) {
	s.applyProxy()
	if err := s.client.Connect(); err != nil {
		return "", err
	}
	code, err := s.client.PairPhone(ctx, phone, true, whatsmeow.PairClientChrome, "AstraCalls")
	if err != nil {
		return "", err
	}
	s.setAuth(AuthSnapshot{State: "pairing_code", Code: code})
	return code, nil
}

func (s *Session) setAuth(a AuthSnapshot) {
	s.mu.Lock()
	s.auth = a
	s.mu.Unlock()
	s.mgr.broker.emitAuthState(s.id, a)
	s.mgr.broker.emitSessionList(s.mgr.infos())
}

// notifyDisconnected avisa — UMA vez por queda — que a sessão do WhatsApp caiu:
// dispara o webhook "session_status" e posta um alerta na inbox do Chatwoot para
// o atendente ver que precisa reconectar. reason descreve o que houve; action é a
// instrução. O guard downAlerted (resetado em events.Connected) evita repetição.
func (s *Session) notifyDisconnected(reason, action string) {
	s.mu.Lock()
	if s.downAlerted {
		s.mu.Unlock()
		return
	}
	s.downAlerted = true
	s.mu.Unlock()

	s.log.Warn("sessão do WhatsApp desconectada — avisando", "reason", reason)
	s.dispatchWebhook("session_status", map[string]any{
		"status": "disconnected", "reason": reason, "session": s.id, "name": s.name,
	})
	text := "⚠️ *AstraCalls — WhatsApp desconectado*\n" +
		"A sessão *" + s.name + "* foi " + reason + ".\n" + action
	s.chatwootAlert(text)
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
		// A ponte nova assume o mesmo leg: fechar a antiga NÃO pode encerrar a chamada
		// (senão o swap de renegociação/transferência derruba tudo via OnTerminalICE).
		oldB.DisableTerminate()
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
	if ac.wsBridge != nil {
		ac.wsBridge.Close()
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
		if ac.wsBridge != nil {
			ac.wsBridge.Close()
		}
		if ac.browserOpus != nil {
			ac.browserOpus.Close()
		}
	}
}

// setWSBridge registra uma ponte WebSocket na chamada ativa. Usa o mesmo
// mecanismo de setBridge: fecha a ponte anterior (WebRTC ou WS) sem disparar
// o encerramento da chamada (DisableTerminate).
func (s *Session) setWSBridge(callID string, b *wsBridge, oc media.Codec) {
	oldWSB, oldB, oldOC, found := s.reg.setWSBridge(callID, b, oc)
	if !found {
		b.Close()
		if oc != nil {
			oc.Close()
		}
		return
	}
	if oldB != nil {
		oldB.DisableTerminate()
		oldB.Close()
	}
	if oldWSB != nil {
		oldWSB.DisableTerminate()
		oldWSB.Close()
	}
	if oldOC != nil {
		oldOC.Close()
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
	case core.CallStateActive, core.CallStateOnHold:
		return StatusConnected // em espera ainda é uma chamada conectada (flag held à parte)
	case core.CallStateEnded:
		return StatusEnded
	case core.CallStateInitiating:
		return StatusStarting
	default:
		return StatusRinging
	}
}

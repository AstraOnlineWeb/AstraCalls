package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
)

type SIPGateway struct {
	server   *sipgo.Server
	client   *sipgo.Client
	dialogUA *sipgo.DialogUA
	sessions *SessionManager

	log *slog.Logger

	mu             sync.RWMutex
	registrations  map[string]*sipRegistration // keyed by sip_user
	activeCalls    map[string]*sipCall         // keyed by SIP Call-ID (SIP->WhatsApp)
	inboundDialogs map[string]*inboundDialog   // keyed by SIP Call-ID (WhatsApp->SIP)
}

type sipRegistration struct {
	sipUser    string
	sessionID  string
	contact    string
	contactURI sip.Uri
	transport  string
}

// inboundDialog representa uma chamada WhatsApp->SIP (somos o UAC).
type inboundDialog struct {
	dialog    *sipgo.DialogClientSession
	sessionID string
	waCallID  string
}

type sipCall struct {
	sipCallID string
	sessionID string
	waCallID  string
	fromTag   string
	toTag     string
	fromURI   string
	toURI     string
	remoteRTP string // remote IP:port for RTP
}

func NewSIPGateway(sessions *SessionManager, log *slog.Logger) (*SIPGateway, error) {
	ua, err := sipgo.NewUA(sipgo.WithUserAgent("AstraCalls-SIP/1.0"))
	if err != nil {
		return nil, fmt.Errorf("sipgo ua: %w", err)
	}
	srv, err := sipgo.NewServer(ua)
	if err != nil {
		return nil, fmt.Errorf("sipgo server: %w", err)
	}
	client, err := sipgo.NewClient(ua)
	if err != nil {
		return nil, fmt.Errorf("sipgo client: %w", err)
	}

	gw := &SIPGateway{
		server:         srv,
		client:         client,
		sessions:       sessions,
		log:            log,
		registrations:  make(map[string]*sipRegistration),
		activeCalls:    make(map[string]*sipCall),
		inboundDialogs: make(map[string]*inboundDialog),
	}
	gw.dialogUA = &sipgo.DialogUA{
		Client: client,
		ContactHDR: sip.ContactHeader{
			Address: sip.Uri{Scheme: "sip", User: "astracalls", Host: getLocalIP(), Port: 5060},
		},
		RewriteContact: true, // envia ao IP de origem do registro (NAT-friendly)
	}

	srv.OnRegister(gw.handleRegister)
	srv.OnInvite(gw.handleInvite)
	srv.OnBye(gw.handleBye)
	srv.OnCancel(gw.handleCancel)

	return gw, nil
}

func (gw *SIPGateway) Start(ctx context.Context, addr string) error {
	gw.log.Info("SIP Gateway starting", "addr", addr)
	go func() {
		if err := gw.server.ListenAndServe(ctx, "udp", addr); err != nil {
			gw.log.Error("SIP UDP listener error", "err", err)
		}
	}()
	go func() {
		if err := gw.server.ListenAndServe(ctx, "tcp", addr); err != nil {
			gw.log.Error("SIP TCP listener error", "err", err)
		}
	}()
	return nil
}

// ========== REGISTER ==========

func (gw *SIPGateway) handleRegister(req *sip.Request, tx sip.ServerTransaction) {
	fromHeader := req.From()
	if fromHeader == nil || fromHeader.Address.User == "" {
		gw.reply(tx, req, 400, "Bad Request")
		return
	}
	sipUser := fromHeader.Address.User

	// Find session by sip_user
	sess := gw.findSessionBySIPUser(sipUser)
	if sess == nil {
		gw.log.Warn("SIP REGISTER: unknown user", "sip_user", sipUser)
		gw.reply(tx, req, 403, "Forbidden")
		return
	}

	// Autenticação Digest MD5 contra a senha SIP da sessão.
	authHeader := req.GetHeader("Authorization")
	if authHeader == nil || !validateDigestAuth(authHeader.Value(), "REGISTER", sess.SIPPass) {
		if authHeader != nil {
			gw.log.Warn("SIP REGISTER: digest inválido", "sip_user", sipUser)
		}
		resp := sip.NewResponseFromRequest(req, 401, "Unauthorized", nil)
		resp.AppendHeader(sip.NewHeader("WWW-Authenticate",
			fmt.Sprintf(`Digest realm="astracalls", nonce="%s", algorithm=MD5, qop="auth"`, generateNonce())))
		_ = tx.Respond(resp)
		return
	}

	contactHeader := req.Contact()
	contact := ""
	var contactURI sip.Uri
	if contactHeader != nil {
		contact = contactHeader.Address.String()
		contactURI = contactHeader.Address
	}

	gw.mu.Lock()
	gw.registrations[sipUser] = &sipRegistration{
		sipUser:    sipUser,
		sessionID:  sess.id,
		contact:    contact,
		contactURI: contactURI,
	}
	gw.mu.Unlock()

	gw.log.Info("SIP REGISTER success", "sip_user", sipUser, "session", sess.id, "contact", contact)

	resp := sip.NewResponseFromRequest(req, 200, "OK", nil)
	resp.AppendHeader(sip.NewHeader("Expires", "3600"))
	_ = tx.Respond(resp)
}

// ========== INVITE ==========

func (gw *SIPGateway) handleInvite(req *sip.Request, tx sip.ServerTransaction) {
	fromHeader := req.From()
	toHeader := req.To()
	if fromHeader == nil || toHeader == nil {
		gw.reply(tx, req, 400, "Bad Request")
		return
	}
	sipCallID := req.CallID().Value()

	sipUser := fromHeader.Address.User
	destPhone := toHeader.Address.User

	// Check if this is a Re-INVITE for an already active call
	gw.mu.Lock()
	sc, exists := gw.activeCalls[sipCallID]
	if exists {
		gw.log.Info("SIP INVITE: Re-INVITE received for existing call", "sip_call_id", sipCallID)
		remoteRTP := extractRTPAddress(string(req.Body()))
		if remoteRTP != "" {
			sc.remoteRTP = remoteRTP
			if s, ok := gw.sessions.Get(sc.sessionID); ok {
				if ac, found := s.reg.get(sc.waCallID); found && ac.rtpBridge != nil {
					addr, err := net.ResolveUDPAddr("udp", remoteRTP)
					if err == nil {
						ac.rtpBridge.mu.Lock()
						ac.rtpBridge.remote = addr
						ac.rtpBridge.mu.Unlock()
					}
				}
			}
		}
		gw.mu.Unlock()

		localIP := getLocalIP()
		localPort := 10000
		if s, ok := gw.sessions.Get(sc.sessionID); ok {
			if ac, found := s.reg.get(sc.waCallID); found && ac.rtpBridge != nil {
				localPort = ac.rtpBridge.LocalPort()
			}
		}
		sdpAnswer := buildSIPSDP(localIP, localPort)
		ok200 := sip.NewResponseFromRequest(req, 200, "OK", []byte(sdpAnswer))
		ok200.AppendHeader(sip.NewHeader("Content-Type", "application/sdp"))
		_ = tx.Respond(ok200)
		return
	}
	gw.mu.Unlock()

	// Lookup registration
	gw.mu.RLock()
	reg, ok := gw.registrations[sipUser]
	gw.mu.RUnlock()
	if !ok {
		gw.log.Warn("SIP INVITE: unregistered user", "sip_user", sipUser)
		gw.reply(tx, req, 403, "Forbidden - Not Registered")
		return
	}

	// Find WhatsApp session
	sess, ok := gw.sessions.Get(reg.sessionID)
	if !ok || sess.client.Store.ID == nil {
		gw.reply(tx, req, 503, "Service Unavailable - WhatsApp Not Connected")
		return
	}

	gw.log.Info("SIP INVITE: initiating WhatsApp call",
		"sip_user", sipUser,
		"dest", destPhone,
		"session", sess.id,
	)

	// Send 100 Trying
	trying := sip.NewResponseFromRequest(req, 100, "Trying", nil)
	_ = tx.Respond(trying)

	// Extract remote RTP from SDP body
	remoteRTP := extractRTPAddress(string(req.Body()))

	// Start WhatsApp call via session
	callID, err := sess.sipStartCall(context.Background(), destPhone, false)
	if err != nil {
		gw.log.Error("SIP INVITE: WhatsApp call failed", "err", err)
		gw.reply(tx, req, 500, "Internal Server Error - "+err.Error())
		return
	}

	// Store SIP call state
	sc = &sipCall{
		sipCallID: sipCallID,
		sessionID: sess.id,
		waCallID:  callID,
		fromURI:   fromHeader.Address.String(),
		toURI:     toHeader.Address.String(),
		remoteRTP: remoteRTP,
	}
	if fromHeader.Params != nil {
		sc.fromTag, _ = fromHeader.Params.Get("tag")
	}

	gw.mu.Lock()
	gw.activeCalls[sipCallID] = sc
	gw.mu.Unlock()

	// Send 180 Ringing
	ringing := sip.NewResponseFromRequest(req, 180, "Ringing", nil)
	_ = tx.Respond(ringing)

	// Local RTP Bridge setup -> CallManager
	ac, found := sess.reg.get(callID)
	if !found {
		gw.reply(tx, req, 500, "Internal Server Error - Call lost")
		return
	}

	rtpBridge, err := NewSIPRTPBridge(callID, remoteRTP)
	if err != nil {
		gw.log.Error("SIP INVITE: RTP Bridge failed", "err", err)
		gw.reply(tx, req, 500, "Internal Server Error - "+err.Error())
		return
	}

	rtpBridge.OnCapturedPCM = func(pcm []float32) {
		ac.cm.FeedCapturedPCM(pcm)
	}
	sess.setRTPBridge(callID, rtpBridge)

	// Build SDP answer for audio (G.711 u-law, dynamic RTP port)
	localIP := getLocalIP()
	sdpAnswer := buildSIPSDP(localIP, rtpBridge.LocalPort())

	// Send 200 OK only when WhatsApp call is connected (Active)
	rtpBridge.OnActive = func() {
		ok200 := sip.NewResponseFromRequest(req, 200, "OK", []byte(sdpAnswer))
		ok200.AppendHeader(sip.NewHeader("Content-Type", "application/sdp"))
		_ = tx.Respond(ok200)
		gw.log.Info("SIP INVITE: Call connected on WhatsApp, sending 200 OK", "sip_call_id", sipCallID, "wa_call_id", callID)
	}

	rtpBridge.OnEnded = func(reason string) {
		// Se a chamada WhatsApp falhou/rejeitou antes de conectar, respondemos erro no INVITE.
		gw.log.Info("SIP INVITE/CALL: WhatsApp call ended", "sip_call_id", sipCallID, "reason", reason)

		gw.mu.Lock()
		if _, ok := gw.activeCalls[sipCallID]; ok {
			delete(gw.activeCalls, sipCallID)
		}
		gw.mu.Unlock()

		// tx.Respond é idempotente; se 200 já foi enviado, este 486 é ignorado pelo stack.
		replyErr := sip.NewResponseFromRequest(req, 486, "Busy Here", nil)
		_ = tx.Respond(replyErr)
	}
}

// ========== BYE ==========

func (gw *SIPGateway) handleBye(req *sip.Request, tx sip.ServerTransaction) {
	sipCallID := req.CallID().Value()

	gw.mu.Lock()
	inb, isInbound := gw.inboundDialogs[sipCallID]
	if isInbound {
		delete(gw.inboundDialogs, sipCallID)
	}
	sc, ok := gw.activeCalls[sipCallID]
	if ok {
		delete(gw.activeCalls, sipCallID)
	}
	gw.mu.Unlock()

	// BYE de uma chamada WhatsApp->SIP (somos o UAC): o softphone desligou.
	if isInbound {
		_ = inb.dialog.ReadBye(req, tx)
		if sess, ok := gw.sessions.Get(inb.sessionID); ok {
			sess.terminateCallByID(inb.waCallID)
		}
		gw.log.Info("SIP BYE (inbound): softphone hung up", "sip_call_id", sipCallID, "wa_call_id", inb.waCallID)
		return
	}

	if !ok {
		gw.reply(tx, req, 481, "Call/Transaction Does Not Exist")
		return
	}

	// End WhatsApp call
	if sess, ok := gw.sessions.Get(sc.sessionID); ok {
		sess.terminateCallByID(sc.waCallID)
	}

	gw.log.Info("SIP BYE: call ended", "sip_call_id", sipCallID, "wa_call_id", sc.waCallID)
	gw.reply(tx, req, 200, "OK")
}

// ========== CANCEL ==========

func (gw *SIPGateway) handleCancel(req *sip.Request, tx sip.ServerTransaction) {
	sipCallID := req.CallID().Value()

	gw.mu.Lock()
	sc, ok := gw.activeCalls[sipCallID]
	if ok {
		delete(gw.activeCalls, sipCallID)
	}
	gw.mu.Unlock()

	if ok {
		if sess, found := gw.sessions.Get(sc.sessionID); found {
			sess.terminateCallByID(sc.waCallID)
		}
	}

	gw.log.Info("SIP CANCEL", "sip_call_id", sipCallID)
	gw.reply(tx, req, 200, "OK")
}

// ========== INBOUND (WhatsApp -> SIP) ==========

// handleInboundCall é chamada quando o WhatsApp recebe uma chamada. Se houver um
// softphone/PBX registrado para a sessão, manda um INVITE para ele tocar; quando
// atende, faz a ponte de áudio e aceita a chamada no WhatsApp.
func (gw *SIPGateway) handleInboundCall(sess *Session, callID, peerNumber string) {
	gw.mu.RLock()
	var reg *sipRegistration
	for _, r := range gw.registrations {
		if r.sessionID == sess.id {
			reg = r
			break
		}
	}
	gw.mu.RUnlock()
	if reg == nil {
		return // nenhum SIP registrado: a chamada segue só para o painel web.
	}

	rtpBridge, err := NewSIPRTPBridge(callID, "")
	if err != nil {
		gw.log.Error("inbound: RTP bridge failed", "err", err)
		return
	}
	sess.setRTPBridge(callID, rtpBridge)

	localIP := getLocalIP()
	sdpOffer := buildSIPSDP(localIP, rtpBridge.LocalPort())

	// From = número do WhatsApp (caller ID no softphone).
	fromHDR := &sip.FromHeader{
		DisplayName: peerNumber,
		Address:     sip.Uri{Scheme: "sip", User: peerNumber, Host: localIP},
		Params:      sip.NewParams(),
	}
	fromHDR.Params.Add("tag", generateRandomString(12))
	ct := sip.NewHeader("Content-Type", "application/sdp")

	d, err := gw.dialogUA.Invite(context.Background(), reg.contactURI, []byte(sdpOffer), fromHDR, ct)
	if err != nil {
		gw.log.Error("inbound: INVITE failed", "err", err)
		sess.terminateCallByID(callID)
		return
	}

	gw.log.Info("inbound: ringing SIP", "sip_user", reg.sipUser, "peer", peerNumber, "wa_call_id", callID)

	go func() {
		waitCtx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()

		sipCallID := d.InviteRequest.CallID().Value()

		// Quando a chamada WhatsApp terminar: cancela o INVITE (se ainda tocando)
		// ou manda BYE (se já atendida).
		rtpBridge.OnEnded = func(reason string) {
			cancel()
			byeCtx, c := context.WithTimeout(context.Background(), 5*time.Second)
			defer c()
			_ = d.Bye(byeCtx)
			gw.mu.Lock()
			delete(gw.inboundDialogs, sipCallID)
			gw.mu.Unlock()
		}

		if err := d.WaitAnswer(waitCtx, sipgo.AnswerOptions{}); err != nil {
			gw.log.Info("inbound: SIP not answered", "err", err)
			sess.terminateCallByID(callID)
			return
		}

		// 200 OK: pega o RTP remoto do softphone e fecha a ponte.
		if remoteRTP := extractRTPAddress(string(d.InviteResponse.Body())); remoteRTP != "" {
			if addr, e := net.ResolveUDPAddr("udp", remoteRTP); e == nil {
				rtpBridge.mu.Lock()
				rtpBridge.remote = addr
				rtpBridge.mu.Unlock()
			}
		}
		if err := d.Ack(context.Background()); err != nil {
			gw.log.Error("inbound: ACK failed", "err", err)
		}

		gw.mu.Lock()
		gw.inboundDialogs[sipCallID] = &inboundDialog{dialog: d, sessionID: sess.id, waCallID: callID}
		gw.mu.Unlock()

		// Atende a chamada no WhatsApp -> áudio começa a fluir.
		if ac, ok := sess.reg.get(callID); ok {
			if err := ac.cm.AcceptCall(context.Background(), callID); err != nil {
				gw.log.Error("inbound: accept WhatsApp call failed", "err", err)
			}
		}
		gw.log.Info("inbound: connected WhatsApp<->SIP", "sip_call_id", sipCallID, "wa_call_id", callID)
	}()
}

// sipUserPart extrai a parte numérica (antes do @) de um JID do WhatsApp.
func sipUserPart(jid string) string {
	if i := strings.IndexByte(jid, '@'); i >= 0 {
		jid = jid[:i]
	}
	if i := strings.IndexByte(jid, ':'); i >= 0 {
		jid = jid[:i]
	}
	if i := strings.IndexByte(jid, '.'); i >= 0 {
		jid = jid[:i]
	}
	return jid
}

// ========== Helpers ==========

func (gw *SIPGateway) findSessionBySIPUser(sipUser string) *Session {
	for _, info := range gw.sessions.infos() {
		if info.SIPUser == sipUser {
			if sess, ok := gw.sessions.Get(info.ID); ok {
				return sess
			}
		}
	}
	return nil
}

func (gw *SIPGateway) reply(tx sip.ServerTransaction, req *sip.Request, code int, reason string) {
	resp := sip.NewResponseFromRequest(req, code, reason, nil)
	_ = tx.Respond(resp)
}

func generateNonce() string {
	return generateRandomString(16)
}

func extractRTPAddress(sdp string) string {
	var ip string
	var port string
	for _, line := range strings.Split(sdp, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "c=IN IP4 ") {
			parts := strings.Fields(line)
			if len(parts) >= 3 {
				ip = parts[2]
			}
		}
		if strings.HasPrefix(line, "m=audio ") {
			parts := strings.Fields(line)
			if len(parts) >= 2 {
				port = parts[1]
			}
		}
	}
	if ip != "" && port != "" {
		return ip + ":" + port
	}
	return ""
}

// getLocalIP devolve o IP anunciado no SDP. Em VPS atrás de NAT, defina
// WACALLS_PUBLIC_IP com o IP público (o valor "auto" cai na detecção local).
func getLocalIP() string {
	if env := strings.TrimSpace(os.Getenv("WACALLS_PUBLIC_IP")); env != "" && env != "auto" {
		return env
	}
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return "127.0.0.1"
	}
	for _, addr := range addrs {
		if ipnet, ok := addr.(*net.IPNet); ok && !ipnet.IP.IsLoopback() && ipnet.IP.To4() != nil {
			return ipnet.IP.String()
		}
	}
	return "127.0.0.1"
}

func buildSIPSDP(localIP string, rtpPort int) string {
	return fmt.Sprintf(`v=0
o=AstraCalls 0 0 IN IP4 %s
s=AstraCalls SIP Gateway
c=IN IP4 %s
t=0 0
m=audio %d RTP/AVP 0
a=rtpmap:0 PCMU/8000
a=ptime:20
a=sendrecv
`, localIP, localIP, rtpPort)
}

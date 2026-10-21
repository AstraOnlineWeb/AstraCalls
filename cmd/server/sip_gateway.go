package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
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
	registrations  map[string]*sipRegistration // keyed by sip_user (modelo 1: PBX registra em nós)
	activeCalls    map[string]*sipCall         // keyed by SIP Call-ID (SIP->WhatsApp)
	inboundDialogs map[string]*inboundDialog   // keyed by SIP Call-ID (WhatsApp->SIP)
	// WhatsApp->SIP ainda tocando no PBX, por callId do WhatsApp: cancela o INVITE
	// quando a chamada é atendida por outro lado (painel, ponte de voz).
	inboundRinging map[string]*inboundRing
	extRegs        map[string]*sipUACRegistrar // keyed by session id (modelo 2: nós registramos no PBX)
	agentCalls     map[string]*sipAgentCall    // keyed by id "sip-…" (modo agente: SIP entrante atendido)
	agentDialogs   map[string]*sipAgentCall    // keyed by SIP Call-ID, vive até o diálogo acabar
}

type sipRegistration struct {
	sourceIP   string // IP de onde veio o REGISTER (INVITE desse IP dispensa nova senha)
	sipUser    string
	sessionID  string
	contact    string
	contactURI sip.Uri
	transport  string
	expiresAt  time.Time // quando este registro expira; registros vencidos são ignorados/limpos
}

func (r *sipRegistration) expired() bool {
	return !r.expiresAt.IsZero() && time.Now().After(r.expiresAt)
}

// inboundRing é o INVITE WhatsApp->SIP ainda sem resposta do PBX.
type inboundRing struct {
	cancel  context.CancelFunc
	claimed bool // atendida por outro lado: o cancelamento não encerra a chamada
}

// cancelInboundRing para o toque no PBX de uma chamada WhatsApp que outro cliente
// (painel, ponte de voz) acabou de atender: envia CANCEL ao PBX e solta a ponte
// RTP do SIP, sem encerrar a chamada no WhatsApp. Devolve se havia toque.
func (gw *SIPGateway) cancelInboundRing(waCallID string) bool {
	gw.mu.Lock()
	r, ok := gw.inboundRinging[waCallID]
	if ok {
		r.claimed = true
		delete(gw.inboundRinging, waCallID)
	}
	gw.mu.Unlock()
	if ok {
		r.cancel()
		gw.log.Info("inbound: atendida por outro cliente, cancelando o toque no PBX", "wa_call_id", waCallID)
	}
	return ok
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
	// diálogo SIP->WhatsApp (somos o UAS): mesma To-tag em todas as respostas,
	// Contact no 2xx, ACK e BYE do jeito que o PBX espera.
	dlg       *sipgo.DialogServerSession
	contact   sip.ContactHeader
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
	// WithClientNAT põe rport no Via: o PBX responde para o IP:porta de onde o
	// pacote saiu (NAT), não para o que anunciamos.
	client, err := sipgo.NewClient(ua, sipgo.WithClientNAT())
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
		inboundRinging: make(map[string]*inboundRing),
		extRegs:        make(map[string]*sipUACRegistrar),
		agentCalls:     make(map[string]*sipAgentCall),
		agentDialogs:   make(map[string]*sipAgentCall),
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
	srv.OnAck(gw.handleAck)
	srv.OnOptions(gw.handleOptions)

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

	// Expires do header (ou do param do Contact). 0 = DESREGISTRAR.
	expires := 3600
	if e := req.GetHeader("Expires"); e != nil {
		if n, err := strconv.Atoi(strings.TrimSpace(e.Value())); err == nil {
			expires = n
		}
	}
	if contactHeader != nil && contactHeader.Params != nil {
		if ev, ok := contactHeader.Params.Get("expires"); ok {
			if n, err := strconv.Atoi(strings.TrimSpace(ev)); err == nil {
				expires = n
			}
		}
	}

	if expires <= 0 {
		// Unregister: remove o binding p/ não deixar registro fantasma tocando num
		// SIP morto (que poderia derrubar chamadas reais de entrada).
		gw.mu.Lock()
		delete(gw.registrations, sipUser)
		gw.mu.Unlock()
		gw.log.Info("SIP REGISTER: desregistrado (Expires 0)", "sip_user", sipUser, "session", sess.id)
		resp := sip.NewResponseFromRequest(req, 200, "OK", nil)
		resp.AppendHeader(sip.NewHeader("Expires", "0"))
		_ = tx.Respond(resp)
		return
	}

	gw.mu.Lock()
	gw.registrations[sipUser] = &sipRegistration{
		sourceIP:   sourceIPOf(req.Source()),
		sipUser:    sipUser,
		sessionID:  sess.id,
		contact:    contact,
		contactURI: contactURI,
		// margem de 32s p/ tolerar atraso do re-REGISTER antes de considerar vencido.
		expiresAt: time.Now().Add(time.Duration(expires+32) * time.Second),
	}
	gw.mu.Unlock()

	gw.log.Info("SIP REGISTER success", "sip_user", sipUser, "session", sess.id, "contact", contact, "expires", expires)

	resp := sip.NewResponseFromRequest(req, 200, "OK", nil)
	// RFC 3261 §10.3: o 200 lista o binding registrado no Contact. PBXs como o
	// 3CX conferem o próprio Contact aqui; sem ele tratam o registro como falho
	// (tronco "Não registrado" e sem rota de saída) mesmo com o 200.
	if contactHeader != nil {
		params := sip.NewParams()
		params.Add("expires", strconv.Itoa(expires))
		resp.AppendHeader(&sip.ContactHeader{Address: contactURI, Params: params})
	}
	resp.AppendHeader(sip.NewHeader("Expires", strconv.Itoa(expires)))
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

	// re-INVITE dentro de um diálogo do modo agente (hold, refresh).
	if c := gw.agentCallBySIP(sipCallID); c != nil {
		gw.agentReInvite(c, req, tx)
		return
	}

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
		pt := rtpPTPCMU
		if s, ok := gw.sessions.Get(sc.sessionID); ok {
			if ac, found := s.reg.get(sc.waCallID); found && ac.rtpBridge != nil {
				localPort = ac.rtpBridge.LocalPort()
				ac.rtpBridge.mu.Lock()
				pt = ac.rtpBridge.pt
				ac.rtpBridge.mu.Unlock()
			}
		}
		sdpAnswer := buildSIPSDPAnswer(localIP, localPort, pt, 0)
		ok200 := sip.NewResponseFromRequest(req, 200, "OK", []byte(sdpAnswer))
		ok200.AppendHeader(sip.NewHeader("Content-Type", "application/sdp"))
		if sc.dlg != nil {
			contact := sc.contact
			ok200.AppendHeader(&contact)
		}
		_ = tx.Respond(ok200)
		return
	}
	gw.mu.Unlock()

	// Descobre a sessão dona do INVITE.
	// Modelo 1: o softphone/PBX está registrado em nós (casa por sip_user).
	// Modelo 2: o INVITE vem de um PBX externo no qual ESTAMOS registrados
	// (casa pelo IP de origem com o host do PBX configurado).
	var sess *Session
	gw.mu.RLock()
	reg, ok := gw.registrations[sipUser]
	gw.mu.RUnlock()
	if ok && reg.expired() {
		ok = false // registro vencido: ignora (evita tocar num SIP morto)
	}
	if ok {
		if s, found := gw.sessions.Get(reg.sessionID); found {
			// o From é só um nome: quem não liga do IP do registro precisa
			// provar a senha SIP da sessão (Digest), senão qualquer um que
			// soubesse o usuário discaria pelo WhatsApp do cliente.
			if !gw.inviteFromRegistration(reg, req) && !gw.challengeInvite(s, req, tx) {
				return
			}
			sess = s
		}
	}
	if sess == nil {
		sess = gw.sessionForExtInvite(req)
	}
	if sess == nil {
		// modelo 1 + modo agente: o PBX registrado em nós liga PARA o usuário SIP
		// da sessão (o From é o chamador). Só aceita vindo do IP desse registro.
		sess = gw.sessionForAgentInvite(toHeader.Address.User, sourceIPOf(req.Source()))
	}
	if sess == nil {
		// modelo 1 com o From = caller ID do ramal (3CX): casa pelo IP do PBX que
		// está registrado em nós.
		sess = gw.sessionForRegisteredPBX(sourceIPOf(req.Source()))
	}
	if sess == nil {
		gw.log.Warn("SIP INVITE: sessão não encontrada", "sip_user", sipUser, "source", req.Source())
		gw.reply(tx, req, 403, "Forbidden - Not Registered")
		return
	}
	// modo agente: atende o INVITE e entrega ao agente de voz (não usa o WhatsApp).
	if wantsAgent(sess, req) {
		gw.handleAgentInvite(sess, req, tx)
		return
	}
	if sess.client.Store.ID == nil {
		gw.reply(tx, req, 503, "Service Unavailable - WhatsApp Not Connected")
		return
	}

	gw.log.Info("SIP INVITE: initiating WhatsApp call",
		"sip_user", sipUser,
		"dest", destPhone,
		"session", sess.id,
	)

	// Diálogo UAS do sipgo: uma To-tag só para 180 e 200 (RFC 3261 §8.2.6.2) e
	// Contact nas respostas (§12.1.1). Sem isso o 3CX não reconhecia o 200 como
	// do mesmo diálogo, derrubava a chamada no atendimento e dava "ocupado".
	dlg, err := gw.dialogUA.ReadInvite(req, tx)
	if err != nil {
		gw.log.Warn("SIP INVITE: inválido", "err", err)
		gw.reply(tx, req, 400, "Bad Request")
		return
	}
	contact := sip.ContactHeader{Address: sip.Uri{
		Scheme: "sip", User: destPhone, Host: getLocalIP(), Port: sipAdvertisePort(),
	}}
	fail := func(code int, reason string) { _ = dlg.Respond(code, reason, nil, &contact) }

	_ = dlg.Respond(100, "Trying", nil)

	// Extract remote RTP from SDP body
	remoteRTP := extractRTPAddress(string(req.Body()))

	// Start WhatsApp call via session
	callID, err := sess.sipStartCall(context.Background(), destPhone, false)
	if err != nil {
		gw.log.Error("SIP INVITE: WhatsApp call failed", "err", err)
		fail(500, "Internal Server Error - "+err.Error())
		return
	}

	// Store SIP call state
	sc = &sipCall{
		sipCallID: sipCallID,
		sessionID: sess.id,
		waCallID:  callID,
		dlg:       dlg,
		contact:   contact,
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

	_ = dlg.Respond(180, "Ringing", nil, &contact)

	// Local RTP Bridge setup -> CallManager
	ac, found := sess.reg.get(callID)
	if !found {
		fail(500, "Internal Server Error - Call lost")
		return
	}

	rtpBridge, err := NewSIPRTPBridge(callID, remoteRTP, gw.log)
	if err != nil {
		gw.log.Error("SIP INVITE: RTP Bridge failed", "err", err)
		fail(500, "Internal Server Error - "+err.Error())
		return
	}

	rtpBridge.SetHandlers(func(pcm []float32) {
		ac.spies.feed(spySideAgent, pcm)
		ac.cm.FeedCapturedPCM(pcm)
	}, nil)
	sess.setRTPBridge(callID, rtpBridge)

	// SDP answer com o codec G.711 que o PBX ofertou (PCMU se não der pra ler a oferta).
	pt := rtpPTPCMU
	if p, ok := parseSDPOffer(string(req.Body())).pickG711(); ok {
		pt = p
	}
	rtpBridge.SetCodec(pt, 0)
	localIP := getLocalIP()
	sdpAnswer := buildSIPSDPAnswer(localIP, rtpBridge.LocalPort(), pt, 0)

	// O handler do sipgo precisa ficar bloqueado até a resposta final: ao
	// retornar, a transação do INVITE é terminada e o 200 OK tardio se perderia.
	settled := make(chan struct{})
	var settleOnce sync.Once
	settle := func() { settleOnce.Do(func() { close(settled) }) }
	defer func() {
		select {
		case <-settled:
		case <-time.After(3 * time.Minute):
		}
	}()

	// Send 200 OK only when WhatsApp call is connected (Active)
	var answered atomic.Bool
	rtpBridge.OnActive = func() {
		answered.Store(true)
		gw.log.Info("SIP INVITE: Call connected on WhatsApp, sending 200 OK", "sip_call_id", sipCallID, "wa_call_id", callID)
		// o 200 retransmite até o ACK chegar (handleAck → ReadAck): não segura o
		// callback do WhatsApp.
		go func() {
			defer settle()
			ct := sip.NewHeader("Content-Type", "application/sdp")
			if err := dlg.Respond(200, "OK", []byte(sdpAnswer), ct, &contact); err != nil {
				gw.log.Warn("SIP INVITE: 200 OK sem ACK, encerrando", "sip_call_id", sipCallID, "err", err)
				sess.terminateCallByID(callID)
			}
		}()
	}

	rtpBridge.OnEnded = func(reason string) {
		gw.log.Info("SIP INVITE/CALL: WhatsApp call ended", "sip_call_id", sipCallID, "reason", reason)

		gw.mu.Lock()
		_, stillActive := gw.activeCalls[sipCallID]
		delete(gw.activeCalls, sipCallID)
		gw.mu.Unlock()

		if answered.Load() {
			// atendida: o WhatsApp desligou -> BYE para o PBX (se foi o PBX que
			// mandou BYE, o diálogo já terminou e o Bye não faz nada). Quem libera o
			// handler é a goroutine do 200.
			if stillActive {
				go func() {
					ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
					defer cancel()
					if err := dlg.Bye(ctx); err != nil {
						gw.log.Warn("SIP BYE para o PBX falhou", "sip_call_id", sipCallID, "err", err)
					}
				}()
			}
			return
		}
		// não atendida: erro final no INVITE. Sem estar mais ativa é porque o PBX
		// cancelou (CANCEL) -> 487; senão o WhatsApp recusou/ocupado/sem resposta.
		defer settle()
		if !stillActive {
			fail(487, "Request Terminated")
			return
		}
		fail(486, "Busy Here")
	}
}

// ========== BYE ==========

func (gw *SIPGateway) handleBye(req *sip.Request, tx sip.ServerTransaction) {
	sipCallID := req.CallID().Value()

	// BYE de uma chamada do modo agente: o chamador desligou.
	if c := gw.agentCallBySIP(sipCallID); c != nil {
		if !byeMatchesDialog(req, dialogTag(c.dlg.InviteRequest.From()), serverDialogTag(c.dlg)) {
			gw.reply(tx, req, 481, "Call/Transaction Does Not Exist")
			return
		}
		if err := c.dlg.ReadBye(req, tx); err != nil {
			gw.reply(tx, req, 481, "Call/Transaction Does Not Exist")
			return
		}
		c.end("remote_hangup", 0)
		return
	}

	gw.mu.Lock()
	inb, isInbound := gw.inboundDialogs[sipCallID]
	if isInbound && inb.dialog != nil && inb.dialog.InviteRequest != nil && inb.dialog.InviteResponse != nil &&
		!byeMatchesDialog(req, dialogTag(inb.dialog.InviteRequest.From()), dialogTag(inb.dialog.InviteResponse.To())) {
		isInbound = false // mesmo Call-ID, outro diálogo: não desliga
		inb = nil
	} else if isInbound {
		delete(gw.inboundDialogs, sipCallID)
	}
	sc, ok := gw.activeCalls[sipCallID]
	if ok && sc.dlg != nil && !byeMatchesDialog(req, dialogTag(sc.dlg.InviteRequest.From()), serverDialogTag(sc.dlg)) {
		ok = false
	} else if ok {
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
	if sc.dlg != nil && sc.dlg.ReadBye(req, tx) == nil {
		return // o diálogo respondeu o 200 e encerrou
	}
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

// ========== MODELO 2: registro em PBX externo (UAC) ==========

// applyExtRegistration (re)inicia o registro da sessão num PBX externo conforme a
// config atual. Chame após criar a sessão, no restore e sempre que a config mudar.
func (gw *SIPGateway) applyExtRegistration(sess *Session) {
	cfg := sess.sipExtSnapshot()

	gw.mu.Lock()
	old := gw.extRegs[sess.id]
	delete(gw.extRegs, sess.id)
	gw.mu.Unlock()
	if old != nil {
		go old.stop() // desregistra o antigo em background (best-effort)
	}

	if !cfg.Enabled || cfg.Host == "" || cfg.User == "" {
		sess.setSIPExtStatus("", "")
		gw.sessions.broker.emitSessionList(gw.sessions.infos())
		return
	}

	r := newSIPUACRegistrar(gw, sess.id, cfg)
	gw.mu.Lock()
	gw.extRegs[sess.id] = r
	gw.mu.Unlock()
}

// removeExtRegistration para o registro de uma sessão (ex.: ao deletá-la).
func (gw *SIPGateway) removeExtRegistration(sessionID string) {
	gw.mu.Lock()
	r := gw.extRegs[sessionID]
	delete(gw.extRegs, sessionID)
	gw.mu.Unlock()
	if r != nil {
		go r.stop()
	}
}

// extRegStatus devolve o estado do registro externo de uma sessão (para o painel).
func (gw *SIPGateway) extRegStatus(sessionID string) (state, lastErr string) {
	gw.mu.RLock()
	r := gw.extRegs[sessionID]
	gw.mu.RUnlock()
	if r == nil {
		return "", ""
	}
	return r.snapshot()
}

// inviteFromRegistration diz se o INVITE veio do mesmo IP do REGISTER desse
// usuário (o comportamento de sempre: o PBX registrado liga sem nova senha).
func (gw *SIPGateway) inviteFromRegistration(reg *sipRegistration, req *sip.Request) bool {
	src := sourceIPOf(req.Source())
	if src == "" {
		return false
	}
	return src == reg.sourceIP || pbxHostMatches(reg.contactURI.Host, src)
}

// challengeInvite exige Digest (senha SIP da sessão) no INVITE. Devolve true
// quando a credencial confere; senão responde 401 com o desafio (o PBX/softphone
// reenvia autenticado) e devolve false.
func (gw *SIPGateway) challengeInvite(sess *Session, req *sip.Request, tx sip.ServerTransaction) bool {
	if auth := req.GetHeader("Authorization"); auth != nil {
		if validateDigestAuth(auth.Value(), "INVITE", sess.SIPPass) {
			return true
		}
		gw.log.Warn("SIP INVITE: digest inválido", "session", sess.id, "source", req.Source())
	} else {
		gw.log.Info("SIP INVITE de IP diferente do registro: pedindo senha", "session", sess.id, "source", req.Source())
	}
	resp := sip.NewResponseFromRequest(req, 401, "Unauthorized", nil)
	resp.AppendHeader(sip.NewHeader("WWW-Authenticate",
		fmt.Sprintf(`Digest realm="astracalls", nonce="%s", algorithm=MD5, qop="auth"`, generateNonce())))
	_ = tx.Respond(resp)
	return false
}

// sessionForExtInvite acha a sessão dona quando um INVITE chega de um PBX externo
// no qual estamos registrados (modelo 2), casando o IP de origem com o host do PBX.
// Com várias sessões no mesmo PBX, escolhe pelo ramal chamado (usuário do
// Request-URI/To = usuário que registramos); sem como decidir, não escolhe — antes
// pegava uma qualquer e a ligação podia cair no cliente errado.
func (gw *SIPGateway) sessionForExtInvite(req *sip.Request) *Session {
	sourceIP := sourceIPOf(req.Source())
	users := map[string]bool{}
	if u := req.Recipient.User; u != "" {
		users[u] = true
	}
	if to := req.To(); to != nil && to.Address.User != "" {
		users[to.Address.User] = true
	}
	gw.mu.RLock()
	defer gw.mu.RUnlock()
	var cands []*Session
	for sid, r := range gw.extRegs {
		if pbxHostMatches(r.host(), sourceIP) {
			if sess, ok := gw.sessions.Get(sid); ok {
				cands = append(cands, sess)
			}
		}
	}
	if len(cands) <= 1 {
		if len(cands) == 1 {
			return cands[0]
		}
		return nil
	}
	var match *Session
	for _, sess := range cands {
		if users[sess.sipExtSnapshot().User] {
			if match != nil {
				gw.log.Warn("SIP INVITE: ramal ambíguo entre sessões do mesmo PBX", "source", sourceIP)
				return nil
			}
			match = sess
		}
	}
	if match == nil {
		gw.log.Warn("SIP INVITE: várias sessões no mesmo PBX e nenhuma com o ramal chamado", "source", sourceIP, "ramal", req.Recipient.User)
	}
	return match
}

// sessionForAgentInvite acha a sessão (modelo 1) cujo usuário SIP é o destino do
// INVITE, desde que o pedido venha do IP em que esse usuário está registrado e a
// sessão esteja num modo que atende para o agente.
func (gw *SIPGateway) sessionForAgentInvite(toUser, sourceIP string) *Session {
	if toUser == "" || sourceIP == "" {
		return nil
	}
	gw.mu.RLock()
	reg, ok := gw.registrations[toUser]
	gw.mu.RUnlock()
	if !ok || reg.expired() || !pbxHostMatches(reg.contactURI.Host, sourceIP) {
		return nil
	}
	sess, found := gw.sessions.Get(reg.sessionID)
	if !found || sess.sipExtSnapshot().Agent.mode() == sipInboundOriginate {
		return nil
	}
	return sess
}

// sessionForRegisteredPBX acha a sessão de um PBX registrado em nós (modelo 1)
// pelo IP de origem do INVITE, comparado com o host do Contact do registro.
// Só casa quando há exatamente uma sessão para aquele IP (sem ambiguidade).
func (gw *SIPGateway) sessionForRegisteredPBX(sourceIP string) *Session {
	if sourceIP == "" {
		return nil
	}
	gw.mu.RLock()
	var regs []*sipRegistration
	for _, r := range gw.registrations {
		if !r.expired() {
			regs = append(regs, r)
		}
	}
	gw.mu.RUnlock()

	sessionID := ""
	for _, r := range regs {
		if !pbxHostMatches(r.contactURI.Host, sourceIP) {
			continue
		}
		if sessionID != "" && sessionID != r.sessionID {
			return nil // mais de uma sessão atrás do mesmo IP
		}
		sessionID = r.sessionID
	}
	if sessionID == "" {
		return nil
	}
	sess, _ := gw.sessions.Get(sessionID)
	return sess
}

// pbxHostMatches compara o host configurado do PBX (pode ser hostname) com o IP
// de origem do INVITE. Casa direto por string ou resolvendo o host para IPs.
func pbxHostMatches(pbxHost, sourceIP string) bool {
	if pbxHost == "" || sourceIP == "" {
		return false
	}
	if strings.EqualFold(pbxHost, sourceIP) {
		return true
	}
	if ips, err := net.LookupHost(pbxHost); err == nil {
		for _, ip := range ips {
			if ip == sourceIP {
				return true
			}
		}
	}
	return false
}

// sourceIPOf extrai só o IP de um "ip:port" (Request.Source()).
func sourceIPOf(src string) string {
	if h, _, err := net.SplitHostPort(src); err == nil {
		return h
	}
	return src
}

// ========== INBOUND (WhatsApp -> SIP) ==========

// handleInboundCall é chamada quando o WhatsApp recebe uma chamada. Se houver um
// softphone/PBX registrado para a sessão, manda um INVITE para ele tocar; quando
// atende, faz a ponte de áudio e aceita a chamada no WhatsApp.
func (gw *SIPGateway) handleInboundCall(sess *Session, callID, peerNumber string) {
	// Descobre para onde tocar:
	// Modelo 1: um softphone/PBX registrado em nós -> manda pro Contact dele.
	// Modelo 2: estamos registrados num PBX externo -> manda pro ramal de destino
	//           configurado (sip_ext_dest) naquele PBX.
	var targetURI sip.Uri
	var targetLabel string
	var nextHop string

	gw.mu.RLock()
	var reg *sipRegistration
	for _, r := range gw.registrations {
		if r.sessionID == sess.id && !r.expired() {
			reg = r
			break
		}
	}
	gw.mu.RUnlock()

	if reg != nil {
		targetURI = reg.contactURI
		targetLabel = reg.sipUser
	} else if cfg := sess.sipExtSnapshot(); cfg.Enabled && cfg.Host != "" && cfg.Dest != "" {
		port := cfg.Port
		if port <= 0 {
			port = 5060
		}
		targetURI = sip.Uri{Scheme: "sip", User: cfg.Dest, Host: cfg.Host, Port: port}
		targetLabel = cfg.Dest + "@" + cfg.Host
		// Outbound proxy: roteia o INVITE pro proxy (next hop), mantendo o ramal/domínio.
		// O Request-URI/To continuam ramal@PBX; o proxy é só o próximo salto do
		// transporte (antes o host do proxy ia no Request-URI e alguns PBX recusavam).
		if px := strings.TrimSpace(cfg.Proxy); px != "" {
			nextHop = sipHostPort(px, port)
			targetLabel = cfg.Dest + "@" + cfg.Host + " via " + px
		}
	} else {
		return // nenhum destino SIP: a chamada segue só para o painel web.
	}

	rtpBridge, err := NewSIPRTPBridge(callID, "", gw.log)
	if err != nil {
		gw.log.Error("inbound: RTP bridge failed", "err", err)
		return
	}
	// Áudio do PBX -> WhatsApp. Sem isto o RTP do ramal chegava e era descartado:
	// quem ligou no WhatsApp não ouvia nada (o outro sentido já ia pelo OnPeerAudio).
	if ac, ok := sess.reg.get(callID); ok {
		rtpBridge.SetHandlers(func(pcm []float32) {
			ac.spies.feed(spySideAgent, pcm)
			ac.cm.FeedCapturedPCM(pcm)
		}, nil)
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

	invite := sip.NewRequest(sip.INVITE, targetURI)
	invite.SetBody([]byte(sdpOffer))
	if targetURI.UriParams != nil {
		if tr, _ := targetURI.UriParams.Get("transport"); tr != "" {
			invite.SetTransport(tr)
		}
	}
	invite.AppendHeader(fromHDR)
	invite.AppendHeader(ct)
	if nextHop != "" {
		invite.SetDestination(nextHop)
	}
	d, err := gw.dialogUA.WriteInvite(context.Background(), invite)
	if err != nil {
		gw.log.Error("inbound: INVITE failed", "err", err)
		sess.terminateCallByID(callID)
		return
	}

	gw.log.Info("inbound: ringing SIP", "target", targetLabel, "peer", peerNumber, "wa_call_id", callID)

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

		ring := &inboundRing{cancel: cancel}
		gw.mu.Lock()
		gw.inboundRinging[callID] = ring
		gw.mu.Unlock()

		err := d.WaitAnswer(waitCtx, sipgo.AnswerOptions{})

		gw.mu.Lock()
		claimed := ring.claimed
		if gw.inboundRinging[callID] == ring {
			delete(gw.inboundRinging, callID)
		}
		gw.mu.Unlock()

		if claimed {
			// outro cliente atendeu: o PBX já recebeu o CANCEL; a chamada segue no
			// WhatsApp, só sem a perna SIP (a ponte RTP sai do registro dela).
			if oldB, found := sess.reg.setRTPBridge(callID, nil); found && oldB != nil {
				oldB.Close()
			}
			if err == nil {
				// corrida: o PBX atendeu junto com o cancelamento — derruba a perna SIP
				_ = d.Ack(context.Background())
				byeCtx, c := context.WithTimeout(context.Background(), 5*time.Second)
				_ = d.Bye(byeCtx)
				c()
			}
			return
		}
		if err != nil {
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
	if sipUser == "" {
		return nil
	}
	gw.sessions.mu.RLock()
	defer gw.sessions.mu.RUnlock()
	for _, sess := range gw.sessions.sessions {
		sess.mu.Lock()
		match := sess.SIPUser == sipUser
		sess.mu.Unlock()
		if match {
			return sess
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

// getLocalIP devolve o IP anunciado no SDP/Contact do SIP: WACALLS_PUBLIC_IP
// fixo, ou o IP público descoberto quando vale "auto" (ver publicip.go). Sem a
// variável, cai no IP da primeira interface (comportamento antigo).
func getLocalIP() string {
	if ip := resolvedPublicIP(nil); ip != "" {
		return ip
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

// dialogTag devolve a tag de um cabeçalho From/To (vazio se não houver).
func dialogTag(h interface{ Value() string }) string {
	switch v := h.(type) {
	case *sip.FromHeader:
		if v != nil {
			t, _ := v.Params.Get("tag")
			return t
		}
	case *sip.ToHeader:
		if v != nil {
			t, _ := v.Params.Get("tag")
			return t
		}
	}
	return ""
}

// byeMatchesDialog confere se o BYE é deste diálogo pelas tags (em qualquer
// direção). Sem tag conhecida, aceita como antes (compatibilidade).
func byeMatchesDialog(req *sip.Request, tagA, tagB string) bool {
	if tagA == "" || tagB == "" {
		return true
	}
	from, to := dialogTag(req.From()), dialogTag(req.To())
	return (from == tagA && to == tagB) || (from == tagB && to == tagA)
}

// serverDialogTag é a nossa To-tag num diálogo em que somos UAS ("" antes do 2xx).
func serverDialogTag(d *sipgo.DialogServerSession) string {
	if d == nil || d.InviteResponse == nil {
		return ""
	}
	return dialogTag(d.InviteResponse.To())
}

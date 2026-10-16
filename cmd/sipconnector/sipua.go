package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
)

type route struct {
	lineID    string
	extension string
}

type sipUA struct {
	s        *server
	ua       *sipgo.UserAgent
	srv      *sipgo.Server
	client   *sipgo.Client
	dialogUA *sipgo.DialogUA
	nonces   *nonceStore

	mu       sync.RWMutex
	regs     map[string]*uacRegistrar // "lineID|ext" (ext vazio = linha)
	bindings map[string]*binding      // lineID -> PABX registrado em nós
	routes   map[string]route         // usuário do Contact -> linha/ramal
}

func newSipUA(s *server) (*sipUA, error) {
	ua, err := sipgo.NewUA(sipgo.WithUserAgent("AstraCalls-SIPConnector/1.0"))
	if err != nil {
		return nil, err
	}
	srv, err := sipgo.NewServer(ua)
	if err != nil {
		return nil, err
	}
	client, err := sipgo.NewClient(ua, sipgo.WithClientHostname(s.cfg.PublicIP))
	if err != nil {
		return nil, err
	}
	u := &sipUA{
		s: s, ua: ua, srv: srv, client: client, nonces: newNonceStore(),
		regs: map[string]*uacRegistrar{}, bindings: map[string]*binding{}, routes: map[string]route{},
	}
	u.dialogUA = &sipgo.DialogUA{
		Client:         client,
		ContactHDR:     sip.ContactHeader{Address: u.contactURI("sipconnector")},
		RewriteContact: true, // responde/manda para o IP de origem (PABX atrás de NAT)
	}
	srv.OnRegister(u.handleRegister)
	srv.OnInvite(u.handleInvite)
	srv.OnAck(u.handleAck)
	srv.OnBye(u.handleBye)
	srv.OnCancel(func(*sip.Request, sip.ServerTransaction) {}) // a camada de transação já respondeu
	srv.OnNotify(u.handleNotify)
	srv.OnOptions(u.replyOK)
	srv.OnInfo(u.replyOK)
	return u, nil
}

func (u *sipUA) start(ctx context.Context) {
	for _, network := range []string{"udp", "tcp"} {
		go func(n string) {
			if err := u.srv.ListenAndServe(ctx, n, u.s.cfg.SIPAddr); err != nil && ctx.Err() == nil {
				u.s.log.Error("SIP listener", "network", n, "err", err)
			}
		}(network)
	}
	u.s.log.Info("SIP no ar", "addr", u.s.cfg.SIPAddr, "public", u.s.cfg.PublicIP)
}

func (u *sipUA) contactURI(user string) sip.Uri {
	return sip.Uri{Scheme: "sip", User: user, Host: u.s.cfg.PublicIP, Port: u.s.cfg.SIPAdvertisePort}
}

func generateID(n int) string {
	b := make([]byte, (n+1)/2)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)[:n]
}

func sourceIP(src string) string {
	if h, _, err := net.SplitHostPort(src); err == nil {
		return h
	}
	return src
}

func (u *sipUA) replyOK(req *sip.Request, tx sip.ServerTransaction) {
	_ = tx.Respond(sip.NewResponseFromRequest(req, 200, "OK", nil))
}

func (u *sipUA) reply(req *sip.Request, tx sip.ServerTransaction, code int, reason string) {
	_ = tx.Respond(sip.NewResponseFromRequest(req, code, reason, nil))
}

// ---- configuração das linhas ----

// applyLine (re)aplica os registros de uma linha: o da linha (register_out) e o de
// cada ramal de agente. Chamado no boot e a cada PUT /api/lines/{id}.
func (u *sipUA) applyLine(l Line) {
	u.removeLine(l.ID, false)
	if !l.Enabled {
		return
	}
	targets := []regTarget{}
	base := regTarget{lineID: l.ID, host: l.Host, port: l.Port, transport: l.Transport, proxy: l.Proxy}
	if l.Mode == modeRegisterOut {
		t := base
		t.user, t.password, t.contactUser = l.User, l.Password, contactUserFor(l.ID, "")
		targets = append(targets, t)
	}
	for _, a := range l.Agents {
		t := base
		t.extension, t.user, t.password, t.contactUser = a.Extension, a.Extension, a.Password, contactUserFor(l.ID, a.Extension)
		targets = append(targets, t)
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	u.routes[contactUserFor(l.ID, "")] = route{lineID: l.ID}
	for _, t := range targets {
		u.routes[t.contactUser] = route{lineID: l.ID, extension: t.extension}
		u.regs[l.ID+"|"+t.extension] = newUACRegistrar(u, t)
	}
}

// removeLine para os registros da linha. dropBinding=false mantém o PABX que já
// registrou em nós (um PUT não deve derrubar o tronco até o próximo REGISTER).
func (u *sipUA) removeLine(lineID string, dropBinding bool) {
	u.mu.Lock()
	defer u.mu.Unlock()
	for k, r := range u.regs {
		if r.t.lineID == lineID {
			r.stop()
			delete(u.regs, k)
		}
	}
	for k, rt := range u.routes {
		if rt.lineID == lineID {
			delete(u.routes, k)
		}
	}
	if dropBinding {
		delete(u.bindings, lineID)
	}
}

type regStatus struct {
	State string `json:"state"`
	Error string `json:"error,omitempty"`
}

func (u *sipUA) lineStatus(l Line) (regStatus, map[string]regStatus) {
	u.mu.RLock()
	defer u.mu.RUnlock()
	agents := map[string]regStatus{}
	if !l.Enabled {
		for _, a := range l.Agents {
			agents[a.Extension] = regStatus{State: regDisabled}
		}
		return regStatus{State: regDisabled}, agents
	}
	var line regStatus
	if l.Mode == modeRegisterIn {
		line.State = regWaiting
		if u.bindings[l.ID].alive() {
			line.State = regRegistered
		}
	} else if r := u.regs[l.ID+"|"]; r != nil {
		line.State, line.Error = r.snapshot()
	}
	for _, a := range l.Agents {
		st := regStatus{State: regRegistering}
		if r := u.regs[l.ID+"|"+a.Extension]; r != nil {
			st.State, st.Error = r.snapshot()
		}
		agents[a.Extension] = st
	}
	return line, agents
}

// ---- REGISTER (modo register_in: o PABX registra no conector) ----

func (u *sipUA) handleRegister(req *sip.Request, tx sip.ServerTransaction) {
	from := req.From()
	if from == nil || from.Address.User == "" {
		u.reply(req, tx, 400, "Bad Request")
		return
	}
	var line *Line
	for _, l := range u.s.store.all() {
		if l.Enabled && l.Mode == modeRegisterIn && l.User == from.Address.User {
			line = &l
			break
		}
	}
	if line == nil {
		u.reply(req, tx, 403, "Forbidden")
		return
	}
	auth := req.GetHeader("Authorization")
	if auth == nil || !u.nonces.checkDigest(auth.Value(), "REGISTER", line.User, line.Password) {
		res := sip.NewResponseFromRequest(req, 401, "Unauthorized", nil)
		res.AppendHeader(sip.NewHeader("WWW-Authenticate", u.nonces.challenge()))
		_ = tx.Respond(res)
		return
	}

	expires := 3600
	if h := req.GetHeader("Expires"); h != nil {
		if n, err := strconv.Atoi(strings.TrimSpace(h.Value())); err == nil {
			expires = n
		}
	}
	contact := req.Contact()
	if contact != nil && contact.Params != nil {
		if v, ok := contact.Params.Get("expires"); ok {
			if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
				expires = n
			}
		}
	}

	u.mu.Lock()
	if expires <= 0 || contact == nil {
		delete(u.bindings, line.ID)
	} else {
		u.bindings[line.ID] = &binding{
			contact: contact.Address, source: req.Source(), transport: strings.ToLower(req.Transport()),
			expiresAt: time.Now().Add(time.Duration(expires+32) * time.Second),
		}
	}
	u.mu.Unlock()
	u.s.log.Info("PABX registrou", "line", line.ID, "source", req.Source(), "expires", expires)

	res := sip.NewResponseFromRequest(req, 200, "OK", nil)
	if contact != nil {
		res.AppendHeader(contact)
	}
	res.AppendHeader(sip.NewHeader("Expires", strconv.Itoa(max(expires, 0))))
	_ = tx.Respond(res)
}

// ---- INVITE ----

// routeInvite descobre a linha (e o ramal do agente, se for para ele):
//  1. pelo usuário do nosso Contact (o PABX manda para o Contact que registramos);
//  2. pelo número discado (ramal do agente, usuário da linha ou DID);
//  3. pela origem, quando só uma linha confia naquele IP.
func (u *sipUA) routeInvite(req *sip.Request) (Line, *Agent, bool) {
	ruser, tuser := req.Recipient.User, ""
	if to := req.To(); to != nil {
		tuser = to.Address.User
	}
	u.mu.RLock()
	rt, ok := u.routes[ruser]
	u.mu.RUnlock()
	if ok {
		if l, found := u.s.store.get(rt.lineID); found && l.Enabled {
			if rt.extension == "" {
				return l, nil, true
			}
			if a := l.agentByExtension(rt.extension); a != nil {
				return l, a, true
			}
		}
	}

	src := sourceIP(req.Source())
	var bySource []Line
	for _, l := range u.s.store.all() {
		if !l.Enabled {
			continue
		}
		for _, n := range []string{ruser, tuser} {
			if n == "" {
				continue
			}
			if a := l.agentByExtension(n); a != nil {
				return l, a, true
			}
			if n == l.User || (l.DID != "" && n == l.DID) {
				return l, nil, true
			}
		}
		if u.trusts(l, src) {
			bySource = append(bySource, l)
		}
	}
	if len(bySource) == 1 {
		return bySource[0], nil, true
	}
	return Line{}, nil, false
}

// trusts: só aceitamos chamada vinda do PABX da linha (o host configurado, o proxy
// ou de onde o PABX registrou em nós) — senão qualquer um na internet ligaria.
func (u *sipUA) trusts(l Line, ip string) bool {
	if u.s.cfg.TrustAnySource {
		return true
	}
	if hostMatches(l.Host, ip) || hostMatches(l.Proxy, ip) {
		return true
	}
	u.mu.RLock()
	b := u.bindings[l.ID]
	u.mu.RUnlock()
	return b.alive() && sourceIP(b.source) == ip
}

func (u *sipUA) handleInvite(req *sip.Request, tx sip.ServerTransaction) {
	// re-INVITE (dentro do diálogo): atualiza o destino da mídia e mantém o codec
	if to := req.To(); to != nil && to.Params != nil {
		if tag, ok := to.Params.Get("tag"); ok && tag != "" {
			u.handleReInvite(req, tx)
			return
		}
	}

	line, agent, ok := u.routeInvite(req)
	if !ok {
		u.reply(req, tx, 404, "Not Found")
		return
	}
	if !u.trusts(line, sourceIP(req.Source())) {
		u.s.log.Warn("INVITE de origem não confiável", "line", line.ID, "source", req.Source())
		u.reply(req, tx, 403, "Forbidden")
		return
	}
	offer, err := parseSDP(string(req.Body()))
	if err != nil {
		u.reply(req, tx, 488, "Not Acceptable Here")
		return
	}
	codec, ok := chooseCodec(offer.Payloads)
	if !ok {
		u.reply(req, tx, 488, "Not Acceptable Here")
		return
	}

	dialog, err := u.dialogUA.ReadInvite(req, tx)
	if err != nil {
		u.reply(req, tx, 400, "Bad Request")
		return
	}
	from := req.From()
	c, err := u.s.newCall(line, "inbound", from.Address.User, from.DisplayName, agent, req.CallID().Value())
	if err != nil {
		_ = dialog.Respond(503, "Service Unavailable", nil)
		return
	}
	c.mu.Lock()
	c.srv = dialog
	c.mu.Unlock()
	c.leg.setCodec(codec)
	c.leg.setRemote(offer.addr())

	_ = dialog.Respond(180, "Ringing", nil)
	c.publish()
	u.s.broker.emit(c.AccountID, map[string]any{
		"type": "incoming", "sessionId": c.LineID, "id": c.ID, "peer": c.Peer, "phone": c.Peer,
		"name": c.Name, "video": false, "offeredAt": c.StartedAt.UnixMilli(),
		"extension": c.Extension, "agentEmail": c.AgentEmail,
	})
	u.s.log.Info("chamada recebida", "call", c.ID, "line", line.ID, "from", c.Peer, "ext", c.Extension)

	c.mu.Lock()
	c.ringTimer = time.AfterFunc(u.s.cfg.RingTimeout, func() {
		if c.currentStatus() == statusRinging {
			_ = dialog.Respond(480, "Temporarily Unavailable", nil)
			c.end("timeout")
		}
	})
	c.mu.Unlock()

	// CANCEL antes de atender e BYE depois encerram o contexto do diálogo
	go func() {
		<-dialog.Context().Done()
		if c.currentStatus() == statusRinging {
			c.end("cancelled")
		} else {
			c.end("remote_hangup")
		}
	}()
	<-c.settled
}

func (u *sipUA) handleReInvite(req *sip.Request, tx sip.ServerTransaction) {
	c := u.s.calls.bySIP(req.CallID().Value())
	if c == nil {
		u.reply(req, tx, 481, "Call/Transaction Does Not Exist")
		return
	}
	if offer, err := parseSDP(string(req.Body())); err == nil {
		c.leg.setRemote(offer.addr())
	}
	res := sip.NewSDPResponseFromRequest(req, []byte(buildSDP(u.s.cfg.PublicIP, c.leg.localPort(), c.leg.codec())))
	res.AppendHeader(&u.dialogUA.ContactHDR)
	_ = tx.Respond(res)
}

func (u *sipUA) handleAck(req *sip.Request, tx sip.ServerTransaction) {
	c := u.s.calls.bySIP(req.CallID().Value())
	if c == nil {
		return
	}
	c.mu.Lock()
	srv := c.srv
	c.mu.Unlock()
	if srv != nil {
		_ = srv.ReadAck(req, tx)
	}
}

func (u *sipUA) handleBye(req *sip.Request, tx sip.ServerTransaction) {
	c := u.s.calls.bySIP(req.CallID().Value())
	if c == nil {
		u.reply(req, tx, 481, "Call/Transaction Does Not Exist")
		return
	}
	c.mu.Lock()
	srv, cli := c.srv, c.cli
	c.mu.Unlock()
	switch {
	case srv != nil:
		_ = srv.ReadBye(req, tx)
	case cli != nil:
		_ = cli.ReadBye(req, tx)
	default:
		u.replyOK(req, tx)
	}
	c.end("remote_hangup")
}

// ---- saída ----

// dial liga para `phone` pelo ramal do agente (se informado e mapeado) ou pela linha.
func (u *sipUA) dial(line Line, agent *Agent, phone, owner string) (*Call, error) {
	host, port, user, password, transport := line.Host, line.Port, line.User, line.Password, line.Transport
	ext := ""
	if agent != nil {
		user, password, ext = agent.Extension, agent.Password, agent.Extension
	}
	destination := ""
	if line.Mode == modeRegisterIn && agent == nil {
		u.mu.RLock()
		b := u.bindings[line.ID]
		u.mu.RUnlock()
		if !b.alive() {
			return nil, errors.New("o PABX não está registrado nesta linha")
		}
		host, port, password, destination = b.contact.Host, b.contact.Port, "", b.source
		if b.transport == "tcp" {
			transport = "tcp"
		}
	}
	if host == "" {
		return nil, errors.New("linha sem servidor do PABX")
	}
	if port == 0 {
		port = 5060
	}

	sipCallID := generateID(24) + "@sipconnector"
	c, err := u.s.newCall(line, "outbound", phone, "", agent, sipCallID)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.owner = &owner
	c.mu.Unlock()

	req := sip.NewRequest(sip.INVITE, sip.Uri{Scheme: "sip", User: phone, Host: host, Port: port})
	applyRouting(req, line.Proxy, port, transport)
	if destination != "" {
		req.SetDestination(destination)
	}
	fromHost := line.Host
	if fromHost == "" {
		fromHost = host
	}
	from := &sip.FromHeader{DisplayName: user, Address: sip.Uri{Scheme: "sip", User: user, Host: fromHost}, Params: sip.NewParams()}
	from.Params.Add("tag", generateID(12))
	to := &sip.ToHeader{Address: sip.Uri{Scheme: "sip", User: phone, Host: host}}
	callID := sip.CallIDHeader(sipCallID)
	contact := &sip.ContactHeader{Address: u.contactURI(contactUserFor(line.ID, ext))}
	req.AppendHeader(from)
	req.AppendHeader(to)
	req.AppendHeader(&callID)
	req.AppendHeader(contact)
	req.AppendHeader(sip.NewHeader("Content-Type", "application/sdp"))
	req.SetBody([]byte(buildSDP(u.s.cfg.PublicIP, c.leg.localPort(), ptPCMU, ptPCMA)))

	ctx, cancel := context.WithCancel(context.Background())
	d, err := u.dialogUA.WriteInvite(ctx, req)
	if err != nil {
		cancel()
		c.end("failed")
		return nil, err
	}
	c.mu.Lock()
	c.cli, c.cancelDial = d, cancel
	c.mu.Unlock()
	c.publish()

	go func() {
		defer cancel()
		err := d.WaitAnswer(ctx, sipgo.AnswerOptions{Username: user, Password: password})
		if err != nil {
			c.end(dialFailureReason(err))
			return
		}
		if ans, perr := parseSDP(string(d.InviteResponse.Body())); perr == nil {
			if codec, ok := chooseCodec(ans.Payloads); ok {
				c.leg.setCodec(codec)
			}
			c.leg.setRemote(ans.addr())
		}
		if err := d.Ack(context.Background()); err != nil {
			u.s.log.Warn("ACK falhou", "call", c.ID, "err", err)
		}
		c.connected()
		<-d.Context().Done()
		c.end("remote_hangup")
	}()
	return c, nil
}

func dialFailureReason(err error) string {
	var resErr *sipgo.ErrDialogResponse
	if errors.As(err, &resErr) && resErr.Res != nil {
		switch code := resErr.Res.StatusCode; {
		case code == 486 || code == 600:
			return "busy"
		case code == 487:
			return "cancelled"
		case code == 480 || code == 408:
			return "no_answer"
		case code == 404 || code == 484:
			return "invalid_number"
		default:
			return fmt.Sprintf("failed_%d", code)
		}
	}
	if errors.Is(err, context.Canceled) {
		return "cancelled"
	}
	return "failed"
}

// ---- transferência (REFER cego para um ramal do PABX) ----

func (u *sipUA) transfer(c *Call, extension string) error {
	line, ok := u.s.store.get(c.LineID)
	if !ok {
		return errors.New("linha não encontrada")
	}
	host := line.Host
	if host == "" {
		u.mu.RLock()
		if b := u.bindings[line.ID]; b.alive() {
			host = b.contact.Host
		}
		u.mu.RUnlock()
	}
	if host == "" {
		return errors.New("não sei o endereço do PABX desta linha")
	}

	c.mu.Lock()
	if c.status != statusConnected {
		c.mu.Unlock()
		return errNotActive
	}
	srv, cli := c.srv, c.cli
	c.transferring = true
	c.mu.Unlock()

	var target sip.Uri
	var do func(context.Context, *sip.Request) (*sip.Response, error)
	switch {
	case srv != nil:
		target, do = srv.InviteRequest.Contact().Address, srv.Do
	case cli != nil && cli.InviteResponse != nil && cli.InviteResponse.Contact() != nil:
		target, do = cli.InviteResponse.Contact().Address, cli.Do
	default:
		c.setTransferring(false)
		return errors.New("diálogo sem destino para o REFER")
	}
	refer := sip.NewRequest(sip.REFER, target)
	refer.AppendHeader(sip.NewHeader("Refer-To", fmt.Sprintf("<sip:%s@%s>", extension, host)))
	refer.AppendHeader(sip.NewHeader("Referred-By", fmt.Sprintf("<sip:%s@%s>", line.User, host)))

	ctx, done := context.WithTimeout(context.Background(), 10*time.Second)
	defer done()
	res, err := do(ctx, refer)
	if err != nil || res.StatusCode >= 300 {
		c.setTransferring(false)
		if err == nil {
			err = fmt.Errorf("o PABX recusou a transferência (SIP %d)", res.StatusCode)
		}
		return err
	}
	u.s.log.Info("transferência aceita pelo PABX", "call", c.ID, "ext", extension)
	// Rede de segurança: se o PABX não mandar NOTIFY nem BYE, volta a chamada ao
	// normal (o consumidor pode desligar de novo).
	time.AfterFunc(30*time.Second, func() { c.setTransferring(false) })
	return nil
}

func (c *Call) setTransferring(v bool) {
	c.mu.Lock()
	c.transferring = v
	c.mu.Unlock()
}

// NOTIFY do REFER: o corpo (message/sipfrag) diz como a transferência terminou.
func (u *sipUA) handleNotify(req *sip.Request, tx sip.ServerTransaction) {
	u.replyOK(req, tx)
	c := u.s.calls.bySIP(req.CallID().Value())
	if c == nil {
		return
	}
	f := strings.Fields(string(req.Body()))
	if len(f) < 2 || !strings.HasPrefix(f[0], "SIP/") {
		return
	}
	code, _ := strconv.Atoi(f[1])
	switch {
	case code >= 200 && code < 300:
		u.s.log.Info("transferência concluída", "call", c.ID)
		c.hangup("transferred")
	case code >= 300:
		u.s.log.Warn("transferência falhou", "call", c.ID, "code", code)
		c.setTransferring(false)
	}
}

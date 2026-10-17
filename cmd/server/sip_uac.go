package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
)

// sipHostPort normaliza "host" ou "host:porta" em "host:porta" (defaultPort quando a
// porta não vier). Usado p/ o destino do outbound proxy.
func sipHostPort(addr string, defaultPort int) string {
	h, p := sipHostPortParts(addr, defaultPort)
	if h == "" {
		return ""
	}
	return net.JoinHostPort(h, strconv.Itoa(p))
}

// sipHostPortParts separa "host[:porta]" em (host, porta), com defaultPort de fallback.
func sipHostPortParts(addr string, defaultPort int) (string, int) {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return "", 0
	}
	if defaultPort <= 0 {
		defaultPort = 5060
	}
	if h, p, err := net.SplitHostPort(addr); err == nil {
		if n, e := strconv.Atoi(p); e == nil && n > 0 {
			return h, n
		}
		return h, defaultPort
	}
	return addr, defaultPort
}

// Modelo 2 (UAC): o AstraCalls se REGISTRA como cliente/ramal num PBX externo do
// cliente. Assim o PBX passa a "ver" a sessão como um ramal registrado e pode
// mandar/receber chamadas de/para o WhatsApp sem precisar registrar de volta em nós.

const (
	sipRegRegistering = "registering"
	sipRegRegistered  = "registered"
	sipRegFailed      = "failed"
)

// intervalo (segundos) de expiração pedido no REGISTER; re-registramos com 80%.
const sipExtDefaultExpires = 300

type sipUACRegistrar struct {
	gw        *SIPGateway
	sessionID string
	callID    string // Call-ID estável entre re-REGISTERs

	ctx    context.Context
	cancel context.CancelFunc

	mu         sync.Mutex
	cfg        sipExtConfig
	state      string
	lastErr    string
	cseq       uint32    // CSeq do REGISTER: cresce sempre (mesmo Call-ID — RFC 3261 §10.3)
	validUntil time.Time // até quando o último registro aceito vale
}

func newSIPUACRegistrar(gw *SIPGateway, sessionID string, cfg sipExtConfig) *sipUACRegistrar {
	ctx, cancel := context.WithCancel(context.Background())
	r := &sipUACRegistrar{
		gw:        gw,
		sessionID: sessionID,
		cfg:       cfg,
		callID:    generateRandomString(24) + "@astracalls",
		ctx:       ctx,
		cancel:    cancel,
		state:     sipRegRegistering,
	}
	go r.loop()
	return r
}

func (r *sipUACRegistrar) host() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.cfg.Host
}

func (r *sipUACRegistrar) snapshot() (state, lastErr string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.state, r.lastErr
}

func (r *sipUACRegistrar) setState(state, errMsg string) {
	r.mu.Lock()
	r.state = state
	r.lastErr = errMsg
	r.mu.Unlock()
	if sess, ok := r.gw.sessions.Get(r.sessionID); ok {
		sess.setSIPExtStatus(state, errMsg)
		r.gw.sessions.broker.emitSessionList(r.gw.sessions.infos())
	}
}

// stop cancela o loop e tenta desregistrar (Expires: 0) em best-effort.
func (r *sipUACRegistrar) stop() {
	r.cancel()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _, _ = r.register(ctx, 0)
}

func (r *sipUACRegistrar) loop() {
	go r.keepAliveLoop()
	backoff := 5 * time.Second
	for {
		if r.ctx.Err() != nil {
			return
		}
		code, expires, err := r.register(r.ctx, sipExtDefaultExpires)
		switch {
		case err == nil && code == 200:
			r.mu.Lock()
			r.validUntil = time.Now().Add(time.Duration(expires) * time.Second)
			r.mu.Unlock()
			r.setState(sipRegRegistered, "")
			r.gw.log.Info("SIP UAC registered", "session", r.sessionID, "host", r.host(), "expires", expires)
			backoff = 5 * time.Second
			// renova com 80% do Expires devolvido pelo PBX.
			wait := time.Duration(expires) * time.Second * 8 / 10
			if wait < 30*time.Second {
				wait = 30 * time.Second
			}
			select {
			case <-r.ctx.Done():
				return
			case <-time.After(wait):
			}
			continue
		case err != nil:
			r.failed(err.Error())
		default:
			r.failed(fmt.Sprintf("registro recusado pelo PBX (SIP %d)", code))
		}
		// caminho de falha: retenta logo (5 s, 10 s, 20 s… até 60 s).
		select {
		case <-r.ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < 60*time.Second {
			backoff *= 2
		}
	}
}

// failed registra a falha de um REGISTER. Enquanto o registro anterior ainda
// vale no PBX, o estado continua "registered" (só uma renovação falhou) — evita
// o painel piscar "falhou" a cada 500/timeout isolado.
func (r *sipUACRegistrar) failed(msg string) {
	r.mu.Lock()
	stillValid := time.Now().Before(r.validUntil)
	callID, cseq := r.callID, r.cseq
	r.mu.Unlock()
	r.gw.log.Warn("SIP UAC register failed", "session", r.sessionID, "host", r.host(),
		"err", msg, "call_id", callID, "cseq", cseq, "still_valid", stillValid)
	if stillValid {
		r.setState(sipRegRegistered, "última renovação falhou: "+msg)
		return
	}
	r.setState(sipRegFailed, msg)
}

// sipKeepAliveSecs: intervalo do OPTIONS de keep-alive ao PBX (mantém o NAT
// aberto para os INVITEs chegarem). 0 desliga.
var sipKeepAliveSecs = envInt("WACALLS_SIP_KEEPALIVE_SECONDS", 25)

func (r *sipUACRegistrar) keepAliveLoop() {
	if sipKeepAliveSecs <= 0 {
		return
	}
	t := time.NewTicker(time.Duration(sipKeepAliveSecs) * time.Second)
	defer t.Stop()
	for {
		select {
		case <-r.ctx.Done():
			return
		case <-t.C:
		}
		if st, _ := r.snapshot(); st != sipRegRegistered {
			continue
		}
		r.mu.Lock()
		cfg := r.cfg
		r.mu.Unlock()
		port := cfg.Port
		if port <= 0 {
			port = 5060
		}
		req := sip.NewRequest(sip.OPTIONS, sip.Uri{Scheme: "sip", Host: cfg.Host, Port: port})
		if px := strings.TrimSpace(cfg.Proxy); px != "" {
			req.SetDestination(sipHostPort(px, port))
		}
		aor := sip.Uri{Scheme: "sip", User: cfg.User, Host: cfg.Host}
		from := &sip.FromHeader{Address: aor, Params: sip.NewParams()}
		from.Params.Add("tag", generateRandomString(12))
		req.AppendHeader(from)
		req.AppendHeader(&sip.ToHeader{Address: sip.Uri{Scheme: "sip", Host: cfg.Host}, Params: sip.NewParams()})
		ctx, cancel := context.WithTimeout(r.ctx, 5*time.Second)
		_, err := r.gw.client.Do(ctx, req) // qualquer resposta (até 401/405) serve
		cancel()
		if err != nil && sipDebugEnabled {
			r.gw.log.Info("SIP UAC keep-alive sem resposta", "session", r.sessionID, "err", err)
		}
	}
}

// register envia um REGISTER (com Digest se desafiado) e devolve o status e o
// Expires efetivo. expires=0 desregistra.
func (r *sipUACRegistrar) register(ctx context.Context, expires int) (code int, effExpires int, err error) {
	r.mu.Lock()
	cfg := r.cfg
	r.mu.Unlock()

	port := cfg.Port
	if port <= 0 {
		port = 5060
	}
	// Request-URI e AOR usam o DOMÍNIO (cfg.Host). Se houver um outbound proxy,
	// os pacotes são enviados PRA ELE (next hop) mantendo a identidade no domínio —
	// é o que FreePBX e troncos hospedados precisam (domínio ≠ servidor).
	recipient := sip.Uri{Scheme: "sip", Host: cfg.Host, Port: port}
	req := sip.NewRequest(sip.REGISTER, recipient)
	if px := strings.TrimSpace(cfg.Proxy); px != "" {
		req.SetDestination(sipHostPort(px, port))
	}

	aor := sip.Uri{Scheme: "sip", User: cfg.User, Host: cfg.Host}
	from := &sip.FromHeader{Address: aor, Params: sip.NewParams()}
	from.Params.Add("tag", generateRandomString(12))
	to := &sip.ToHeader{Address: aor, Params: sip.NewParams()}
	contact := &sip.ContactHeader{
		Address: sip.Uri{Scheme: "sip", User: cfg.User, Host: sipAdvertiseHost(), Port: sipAdvertisePort()},
		Params:  sip.NewParams(),
	}
	callID := sip.CallIDHeader(r.callID)
	r.mu.Lock()
	r.cseq++
	cseq := &sip.CSeqHeader{SeqNo: r.cseq, MethodName: sip.REGISTER}
	r.mu.Unlock()

	req.AppendHeader(from)
	req.AppendHeader(to)
	req.AppendHeader(contact)
	req.AppendHeader(&callID)
	req.AppendHeader(cseq)
	req.AppendHeader(sip.NewHeader("Expires", strconv.Itoa(expires)))

	res, err := r.gw.client.Do(ctx, req)
	if err != nil {
		return 0, 0, err
	}
	if res.StatusCode == 401 || res.StatusCode == 407 {
		res, err = r.gw.client.DoDigestAuth(ctx, req, res, sipgo.DigestAuth{Username: cfg.User, Password: cfg.Pass})
		// o reenvio com Authorization incrementa o CSeq: acompanha o contador.
		if h := req.CSeq(); h != nil {
			r.mu.Lock()
			if h.SeqNo > r.cseq {
				r.cseq = h.SeqNo
			}
			r.mu.Unlock()
		}
		if err != nil {
			return 0, 0, err
		}
	}

	eff := expires
	if h := res.GetHeader("Expires"); h != nil {
		if n, e := strconv.Atoi(strings.TrimSpace(h.Value())); e == nil && n > 0 {
			eff = n
		}
	}
	return res.StatusCode, eff, nil
}

// sipAdvertiseHost é o IP/host anunciado no Contact do REGISTER (onde o PBX vai
// mandar os INVITEs de volta). Em VPS atrás de NAT use WACALLS_PUBLIC_IP.
func sipAdvertiseHost() string {
	return getLocalIP()
}

// sipAdvertisePort é a porta SIP local publicada (padrão 5060; casa com o
// mode=host do Swarm). Configurável via WACALLS_SIP_ADVERTISE_PORT.
func sipAdvertisePort() int {
	if v := strings.TrimSpace(os.Getenv("WACALLS_SIP_ADVERTISE_PORT")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 65535 {
			return n
		}
	}
	return 5060
}

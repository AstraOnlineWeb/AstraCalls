package main

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
)

// Modelo 2 (UAC): o AstraCalls se REGISTRA como cliente/ramal num PBX externo do
// cliente. Assim o PBX passa a "ver" a sessão como um ramal registrado e pode
// mandar/receber chamadas de/para o WhatsApp sem precisar registrar de volta em nós.

const (
	sipRegRegistering = "registering"
	sipRegRegistered  = "registered"
	sipRegFailed      = "failed"
)

// intervalo (segundos) de expiração pedido no REGISTER; re-registramos na metade.
const sipExtDefaultExpires = 300

type sipUACRegistrar struct {
	gw        *SIPGateway
	sessionID string
	callID    string // Call-ID estável entre re-REGISTERs

	ctx    context.Context
	cancel context.CancelFunc

	mu      sync.Mutex
	cfg     sipExtConfig
	state   string
	lastErr string
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
	backoff := 15 * time.Second
	for {
		if r.ctx.Err() != nil {
			return
		}
		code, expires, err := r.register(r.ctx, sipExtDefaultExpires)
		switch {
		case err != nil:
			r.setState(sipRegFailed, err.Error())
			r.gw.log.Warn("SIP UAC register failed", "session", r.sessionID, "host", r.host(), "err", err)
		case code == 200:
			r.setState(sipRegRegistered, "")
			r.gw.log.Info("SIP UAC registered", "session", r.sessionID, "host", r.host(), "expires", expires)
			backoff = 15 * time.Second
			wait := time.Duration(expires/2) * time.Second
			if wait < 60*time.Second {
				wait = 60 * time.Second
			}
			select {
			case <-r.ctx.Done():
				return
			case <-time.After(wait):
			}
			continue
		default:
			msg := fmt.Sprintf("registro recusado pelo PBX (SIP %d)", code)
			r.setState(sipRegFailed, msg)
			r.gw.log.Warn("SIP UAC register rejected", "session", r.sessionID, "host", r.host(), "code", code)
		}
		// caminho de falha: espera com backoff exponencial e tenta de novo.
		select {
		case <-r.ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < 2*time.Minute {
			backoff *= 2
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
	recipient := sip.Uri{Scheme: "sip", Host: cfg.Host, Port: port}
	req := sip.NewRequest(sip.REGISTER, recipient)

	aor := sip.Uri{Scheme: "sip", User: cfg.User, Host: cfg.Host}
	from := &sip.FromHeader{Address: aor, Params: sip.NewParams()}
	from.Params.Add("tag", generateRandomString(12))
	to := &sip.ToHeader{Address: aor, Params: sip.NewParams()}
	contact := &sip.ContactHeader{
		Address: sip.Uri{Scheme: "sip", User: cfg.User, Host: sipAdvertiseHost(), Port: sipAdvertisePort()},
		Params:  sip.NewParams(),
	}
	callID := sip.CallIDHeader(r.callID)

	req.AppendHeader(from)
	req.AppendHeader(to)
	req.AppendHeader(contact)
	req.AppendHeader(&callID)
	req.AppendHeader(sip.NewHeader("Expires", strconv.Itoa(expires)))

	res, err := r.gw.client.Do(ctx, req)
	if err != nil {
		return 0, 0, err
	}
	if res.StatusCode == 401 || res.StatusCode == 407 {
		res, err = r.gw.client.DoDigestAuth(ctx, req, res, sipgo.DigestAuth{Username: cfg.User, Password: cfg.Pass})
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

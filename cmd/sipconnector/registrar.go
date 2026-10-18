package main

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
)

// Registro no PABX (o conector vira um ramal registrado): usado pela linha no modo
// register_out e por CADA ramal de agente. Portado do sip_uac.go do wacalls, com
// Contact próprio por ramal — é o usuário do Contact que diz, no INVITE que o PABX
// manda de volta, para qual linha/ramal a ligação é.
const (
	regRegistering = "registering"
	regRegistered  = "registered"
	regFailed      = "failed"
	regWaiting     = "waiting" // register_in: esperando o PABX registrar
	regDisabled    = "disabled"

	regExpires = 300
)

type regTarget struct {
	lineID      string
	extension   string // vazio = a própria linha
	host        string
	port        int
	transport   string
	proxy       string
	user        string // ramal/AOR: From, To e Contact
	authUser    string // login do digest (vazio = user)
	password    string
	contactUser string
}

type uacRegistrar struct {
	ua     *sipUA
	t      regTarget
	callID string
	ctx    context.Context
	cancel context.CancelFunc

	mu      sync.Mutex
	state   string
	lastErr string
}

// contactUserFor gera um usuário de Contact estável e único por linha/ramal.
func contactUserFor(lineID, ext string) string {
	sum := sha1.Sum([]byte(lineID + "|" + ext))
	return "sc" + hex.EncodeToString(sum[:])[:12]
}

func newUACRegistrar(ua *sipUA, t regTarget) *uacRegistrar {
	return newUACRegistrarAfter(ua, t, nil)
}

// newUACRegistrarAfter só começa a registrar depois de `after` fechar: ao trocar
// a config de um ramal, o Contact é o mesmo, e o "Expires 0" do registrador antigo
// chegando DEPOIS do REGISTER novo fazia o PABX apagar o registro recém-feito.
func newUACRegistrarAfter(ua *sipUA, t regTarget, after <-chan struct{}) *uacRegistrar {
	ctx, cancel := context.WithCancel(context.Background())
	r := &uacRegistrar{
		ua: ua, t: t, ctx: ctx, cancel: cancel, state: regRegistering,
		callID: generateID(24) + "@sipconnector",
	}
	go func() {
		if after != nil {
			select {
			case <-after:
			case <-ctx.Done():
				return
			}
		}
		r.loop()
	}()
	return r
}

func (r *uacRegistrar) snapshot() (string, string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.state, r.lastErr
}

func (r *uacRegistrar) set(state, errMsg string) {
	r.mu.Lock()
	r.state, r.lastErr = state, errMsg
	r.mu.Unlock()
}

// stop cancela o laço e desregistra (Expires 0) em segundo plano. O canal fecha
// quando o desregistro terminou (ou desistiu), para um sucessor com o mesmo
// Contact só registrar depois.
func (r *uacRegistrar) stop() <-chan struct{} {
	r.cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _, _ = r.register(ctx, 0)
	}()
	return done
}

func (r *uacRegistrar) loop() {
	backoff := 15 * time.Second
	for r.ctx.Err() == nil {
		code, expires, err := r.register(r.ctx, regExpires)
		switch {
		case err != nil:
			r.set(regFailed, err.Error())
			r.ua.s.log.Warn("registro no PABX falhou", "line", r.t.lineID, "ext", r.t.extension, "err", err)
		case code == 200:
			r.set(regRegistered, "")
			backoff = 15 * time.Second
			// renova na metade do prazo concedido (piso de 15 s): com piso de 60 s,
			// um PABX que concede 60 s via o registro vencer antes da renovação.
			wait := max(time.Duration(expires/2)*time.Second, 15*time.Second)
			select {
			case <-r.ctx.Done():
				return
			case <-time.After(wait):
			}
			continue
		default:
			r.set(regFailed, fmt.Sprintf("o PABX recusou o registro (SIP %d)", code))
		}
		select {
		case <-r.ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, 2*time.Minute)
	}
}

func (r *uacRegistrar) register(ctx context.Context, expires int) (int, int, error) {
	t := r.t
	recipient := sip.Uri{Scheme: "sip", Host: t.host, Port: t.port}
	req := sip.NewRequest(sip.REGISTER, recipient)
	applyRouting(req, t.proxy, t.port, t.transport)

	aor := sip.Uri{Scheme: "sip", User: t.user, Host: t.host}
	from := &sip.FromHeader{Address: aor, Params: sip.NewParams()}
	from.Params.Add("tag", generateID(12))
	to := &sip.ToHeader{Address: aor, Params: sip.NewParams()}
	contact := &sip.ContactHeader{Address: r.ua.contactURI(t.contactUser), Params: sip.NewParams()}
	callID := sip.CallIDHeader(r.callID)
	req.AppendHeader(from)
	req.AppendHeader(to)
	req.AppendHeader(contact)
	req.AppendHeader(&callID)
	req.AppendHeader(sip.NewHeader("Expires", strconv.Itoa(expires)))

	res, err := r.ua.client.Do(ctx, req)
	if err != nil {
		return 0, 0, err
	}
	if res.StatusCode == 401 || res.StatusCode == 407 {
		res, err = r.ua.client.DoDigestAuth(ctx, req, res, sipgo.DigestAuth{Username: authOr(t.authUser, t.user), Password: t.password})
		if err != nil {
			return 0, 0, err
		}
	}
	return res.StatusCode, grantedExpires(res, expires), nil
}

// grantedExpires é o prazo que o PABX concedeu ao registro. O 3CX manda no
// `expires` do Contact (ex.: 60) e não no header Expires — sem ler o Contact,
// valia o pedido (300 s), a renovação saía aos 150 s e o ramal ficava "Não
// registrado" no PABX a maior parte do tempo.
func grantedExpires(res *sip.Response, requested int) int {
	if res == nil {
		return requested
	}
	for _, h := range res.GetHeaders("Contact") {
		if ch, ok := h.(*sip.ContactHeader); ok && ch.Params != nil {
			if v, ok := ch.Params.Get("expires"); ok {
				if n, e := strconv.Atoi(strings.TrimSpace(v)); e == nil && n > 0 {
					return n
				}
			}
		}
	}
	if h := res.GetHeader("Expires"); h != nil {
		if n, e := strconv.Atoi(strings.TrimSpace(h.Value())); e == nil && n > 0 {
			return n
		}
	}
	return requested
}

// applyRouting: outbound proxy vira o próximo salto (a identidade continua no
// domínio do PABX) e TCP quando configurado.
func applyRouting(req *sip.Request, proxy string, defPort int, transport string) {
	if proxy = strings.TrimSpace(proxy); proxy != "" {
		req.SetDestination(hostPort(proxy, defPort))
	}
	if transport == "tcp" {
		req.SetTransport("TCP")
	}
}

func hostPort(addr string, defPort int) string {
	if _, _, err := net.SplitHostPort(addr); err == nil {
		return addr
	}
	return net.JoinHostPort(addr, strconv.Itoa(defPort))
}

// binding: o PABX registrado no conector (modo register_in).
type binding struct {
	contact   sip.Uri
	source    string // ip:porta de onde o REGISTER veio (é para lá que mandamos)
	transport string
	expiresAt time.Time
}

func (b *binding) alive() bool { return b != nil && time.Now().Before(b.expiresAt) }

// hostMatches compara um host configurado (pode ser nome) com um IP de origem.
func hostMatches(host, ip string) bool {
	host = strings.TrimSpace(host)
	if host == "" || ip == "" {
		return false
	}
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	if strings.EqualFold(host, ip) {
		return true
	}
	ips, err := net.LookupHost(host)
	if err != nil {
		return false
	}
	for _, x := range ips {
		if x == ip {
			return true
		}
	}
	return false
}

package main

import (
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/emiago/sipgo/sip"
)

// fakeServerTx guarda as respostas; o resto da interface não é usado.
type fakeServerTx struct {
	sip.ServerTransaction
	resps []*sip.Response
}

func (f *fakeServerTx) Respond(res *sip.Response) error {
	f.resps = append(f.resps, res)
	return nil
}

// reInvite monta um INVITE de dentro do diálogo (tag no To), como o Asterisk
// manda depois de atender: From = ramal/DID, To = quem ligou no WhatsApp.
func reInvite(callID, body string) *sip.Request {
	req := sip.NewRequest(sip.INVITE, sip.Uri{User: "5561999999999", Host: "10.0.0.2", Port: 5062})
	from := &sip.FromHeader{Address: sip.Uri{User: "5561888888888", Host: "10.0.0.9"}, Params: sip.NewParams()}
	from.Params.Add("tag", "pbx-tag")
	to := &sip.ToHeader{Address: sip.Uri{User: "5561999999999", Host: "10.0.0.2"}, Params: sip.NewParams()}
	to.Params.Add("tag", "our-tag")
	cid := sip.CallIDHeader(callID)
	req.AppendHeader(from)
	req.AppendHeader(to)
	req.AppendHeader(&cid)
	req.AppendHeader(&sip.CSeqHeader{SeqNo: 2, MethodName: sip.INVITE})
	req.AppendHeader(&sip.ViaHeader{ProtocolName: "SIP", ProtocolVersion: "2.0", Transport: "UDP", Host: "10.0.0.9", Port: 5060, Params: sip.NewParams()})
	req.SetBody([]byte(body))
	return req
}

func TestReInviteDaChamadaRecebidaNaoLigaDeVolta(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	br, err := NewSIPRTPBridge("WA1", "", log)
	if err != nil {
		t.Fatal(err)
	}
	defer br.Close()
	sess := &Session{id: "s1", reg: newCallRegistry()}
	sess.reg.add("WA1", &activeCall{rtpBridge: br})
	mgr := &SessionManager{sessions: map[string]*Session{"s1": sess}}
	gw := &SIPGateway{
		sessions:       mgr,
		log:            log,
		activeCalls:    map[string]*sipCall{},
		inboundDialogs: map[string]*inboundDialog{"cid-1": {sessionID: "s1", waCallID: "WA1"}},
	}

	sdp := "v=0\r\no=- 1 2 IN IP4 10.0.0.50\r\ns=-\r\nc=IN IP4 10.0.0.50\r\nt=0 0\r\nm=audio 12000 RTP/AVP 0\r\na=rtpmap:0 PCMU/8000\r\n"
	tx := &fakeServerTx{}
	gw.handleInvite(reInvite("cid-1", sdp), tx)
	if len(tx.resps) != 1 || tx.resps[0].StatusCode != 200 {
		t.Fatalf("re-INVITE da chamada recebida deveria ter 200 OK, veio %v", tx.resps)
	}
	if !strings.Contains(string(tx.resps[0].Body()), "m=audio") {
		t.Fatalf("200 OK sem SDP")
	}
	br.mu.Lock()
	remote := br.remote
	br.mu.Unlock()
	if remote == nil || remote.String() != "10.0.0.50:12000" {
		t.Fatalf("RTP deveria seguir o novo destino, ficou %v", remote)
	}
	if len(gw.activeCalls) != 0 {
		t.Fatalf("não pode originar chamada nova a partir do re-INVITE")
	}

	// re-INVITE de diálogo desconhecido: 481, nunca liga para o To.
	tx = &fakeServerTx{}
	gw.handleInvite(reInvite("cid-desconhecido", sdp), tx)
	if len(tx.resps) != 1 || tx.resps[0].StatusCode != 481 {
		t.Fatalf("diálogo desconhecido deveria dar 481, veio %v", tx.resps)
	}
}

package main

import (
	"strings"
	"testing"
	"time"

	"github.com/emiago/sipgo/sip"
)

func TestG711RoundTrip(t *testing.T) {
	for _, pt := range []uint8{ptPCMU, ptPCMA} {
		for _, s := range []int16{0, 100, -100, 1000, -1000, 8000, -8000, 30000, -30000} {
			got := decodeG711(encodeG711([]int16{s}, pt), pt)[0]
			diff := int(got) - int(s)
			if diff < 0 {
				diff = -diff
			}
			// G.711 é logarítmico: o erro cresce com a amplitude (~3%)
			limit := 16 + int(abs16(s))/25
			if diff > limit {
				t.Errorf("pt=%d s=%d decodificou %d (erro %d > %d)", pt, s, got, diff, limit)
			}
		}
	}
}

func abs16(v int16) int16 {
	if v < 0 {
		return -v
	}
	return v
}

func TestAlawUlawConversionKeepsSignal(t *testing.T) {
	for _, s := range []int16{0, 500, -500, 12000, -12000} {
		alaw := encodeG711([]int16{s}, ptPCMA)
		ulaw := toPCMU(alaw, ptPCMA)
		back := fromPCMU(ulaw, ptPCMA)
		a := decodeG711(alaw, ptPCMA)[0]
		b := decodeG711(back, ptPCMA)[0]
		if d := int(a) - int(b); d > 300 || d < -300 {
			t.Errorf("s=%d: alaw %d -> ulaw -> alaw %d", s, a, b)
		}
	}
}

func TestResampleSizes(t *testing.T) {
	in := make([]int16, 160)
	up := upsample8to16(in, 0)
	if len(up) != 320 {
		t.Fatalf("upsample: %d", len(up))
	}
	if len(downsample16to8(up)) != 160 {
		t.Fatal("downsample")
	}
	if len(bytesToInt16(int16ToBytes(up))) != 320 {
		t.Fatal("bytes")
	}
}

func TestParseSDPAndChooseCodec(t *testing.T) {
	body := "v=0\r\no=- 1 1 IN IP4 10.0.0.1\r\ns=-\r\nc=IN IP4 10.0.0.9\r\nt=0 0\r\n" +
		"m=audio 4000 RTP/AVP 8 0 101\r\na=rtpmap:101 telephone-event/8000\r\n"
	a, err := parseSDP(body)
	if err != nil {
		t.Fatal(err)
	}
	if a.addr() != "10.0.0.9:4000" {
		t.Fatalf("addr %s", a.addr())
	}
	if c, ok := chooseCodec(a.Payloads); !ok || c != ptPCMU {
		t.Fatalf("codec %d %v", c, ok)
	}
	if c, ok := chooseCodec([]int{8, 101}); !ok || c != ptPCMA {
		t.Fatalf("só PCMA: %d %v", c, ok)
	}
	if _, ok := chooseCodec([]int{9, 18}); ok {
		t.Fatal("G722/G729 não deveriam ser aceitos")
	}
	built := buildSDP("1.2.3.4", 5000, ptPCMA)
	back, err := parseSDP(built)
	if err != nil || back.addr() != "1.2.3.4:5000" || back.Payloads[0] != 8 {
		t.Fatalf("buildSDP: %v %+v", err, back)
	}
}

func TestDigestRequiresIssuedNonce(t *testing.T) {
	n := newNonceStore()
	nonce := n.issue()
	ha1 := md5hex("ramal:" + digestRealm + ":segredo")
	ha2 := md5hex("REGISTER:sip:pbx")
	resp := md5hex(ha1 + ":" + nonce + ":00000001:abc:auth:" + ha2)
	auth := `Digest username="ramal", realm="` + digestRealm + `", nonce="` + nonce +
		`", uri="sip:pbx", response="` + resp + `", qop=auth, nc=00000001, cnonce="abc"`
	if !n.checkDigest(auth, "REGISTER", "ramal", "segredo") {
		t.Fatal("digest válido recusado")
	}
	if n.checkDigest(auth, "REGISTER", "ramal", "errada") {
		t.Fatal("senha errada aceita")
	}
	forjado := strings.Replace(auth, nonce, "naoemitido", 1)
	if n.checkDigest(forjado, "REGISTER", "ramal", "segredo") {
		t.Fatal("nonce não emitido aceito")
	}
	n.mu.Lock()
	n.nonces[nonce] = time.Now().Add(-time.Second)
	n.mu.Unlock()
	if n.checkDigest(auth, "REGISTER", "ramal", "segredo") {
		t.Fatal("nonce vencido aceito")
	}
}

func TestWidgetTokenScope(t *testing.T) {
	s := &server{cfg: config{APIKey: "mestra"}}
	tok := s.signWidgetToken(widgetClaims{AccountID: 7, Exp: time.Now().Add(time.Hour).Unix()})
	c, ok := s.parseWidgetToken(tok)
	if !ok || c.AccountID != 7 {
		t.Fatal("token válido recusado")
	}
	if _, ok := s.parseWidgetToken(tok + "x"); ok {
		t.Fatal("assinatura adulterada aceita")
	}
	outro := &server{cfg: config{APIKey: "outra"}}
	if _, ok := outro.parseWidgetToken(tok); ok {
		t.Fatal("token de outra chave-mestra aceito")
	}
	vencido := s.signWidgetToken(widgetClaims{AccountID: 7, Exp: time.Now().Add(-time.Minute).Unix()})
	if _, ok := s.parseWidgetToken(vencido); ok {
		t.Fatal("token vencido aceito")
	}
	p := principal{accountID: 7}
	if !p.canSee(7) || p.canSee(8) {
		t.Fatal("escopo de conta")
	}
}

func TestLineNormalize(t *testing.T) {
	l := Line{ID: "inbox16", AccountID: 1, Mode: "register_out", Host: "pbx.local", User: "100", Password: "x",
		Agents: []Agent{{Email: " Luiz@MTech.net.br ", Extension: "201", Password: "p"}, {Email: "", Extension: "202"}}}
	if err := l.normalize(nil); err != nil {
		t.Fatal(err)
	}
	if l.Port != 5060 || l.Transport != "udp" || len(l.Agents) != 1 || l.Agents[0].Email != "luiz@mtech.net.br" {
		t.Fatalf("%+v", l)
	}
	// senha vazia no PUT mantém a anterior (o AstraChat não recebe a senha de volta)
	novo := Line{ID: "inbox16", AccountID: 1, Host: "pbx.local", User: "100",
		Agents: []Agent{{Email: "luiz@mtech.net.br", Extension: "201"}}}
	if err := novo.normalize(&l); err != nil {
		t.Fatal(err)
	}
	if novo.Password != "x" || novo.Agents[0].Password != "p" {
		t.Fatal("senhas anteriores perdidas")
	}
	if _, ok := novo.public()["password"]; ok {
		t.Fatal("senha exposta")
	}
	if err := (&Line{ID: "a b", AccountID: 1}).normalize(nil); err == nil {
		t.Fatal("id inválido aceito")
	}
	if err := (&Line{ID: "x", AccountID: 1, Mode: modeRegisterIn, User: "tronco"}).normalize(nil); err == nil {
		t.Fatal("register_in sem senha aceito")
	}
	if err := (&Line{ID: "x", AccountID: 1, Mode: modeRegisterIn, User: "t", Password: "s",
		Agents: []Agent{{Email: "a@b", Extension: "1"}}}).normalize(nil); err == nil {
		t.Fatal("ramal de agente sem host aceito")
	}
}

func TestDialable(t *testing.T) {
	if got := dialable("+55 (61) 99999-0000"); got != "5561999990000" {
		t.Fatal(got)
	}
	if got := dialable("*21#"); got != "*21#" {
		t.Fatal(got)
	}
}

// O 3CX concede o prazo no `expires` do Contact, não no header Expires.
func TestGrantedExpires(t *testing.T) {
	parse := func(raw string) *sip.Response {
		t.Helper()
		m, err := sip.ParseMessage([]byte(strings.ReplaceAll(raw, "\n", "\r\n")))
		if err != nil {
			t.Fatal(err)
		}
		return m.(*sip.Response)
	}
	base := "SIP/2.0 200 OK\nVia: SIP/2.0/UDP 10.0.0.1:5070;branch=z9hG4bK1\nFrom: <sip:16719@pbx>;tag=a\nTo: <sip:16719@pbx>;tag=b\nCall-ID: x\nCSeq: 2 REGISTER\n"
	cases := []struct {
		raw  string
		want int
	}{
		{base + "Contact: <sip:inbox20@1.2.3.4:5070>;expires=60\nContent-Length: 0\n\n", 60},
		{base + "Expires: 120\nContent-Length: 0\n\n", 120},
		{base + "Contact: <sip:inbox20@1.2.3.4:5070>;expires=90\nExpires: 3600\nContent-Length: 0\n\n", 90},
		{base + "Content-Length: 0\n\n", 300},
	}
	for i, c := range cases {
		if got := grantedExpires(parse(c.raw), 300); got != c.want {
			t.Fatalf("caso %d: got %d, want %d", i, got, c.want)
		}
	}
}

package main

import (
	"testing"

	waBinary "go.mau.fi/whatsmeow/binary"
	waLog "go.mau.fi/whatsmeow/util/log"
)

func TestNonceTapLogger(t *testing.T) {
	for len(bizNonceCh) > 0 {
		<-bizNonceCh
	}
	l := newNonceTapLogger(waLog.Noop).Sub("Recv")
	l.Debugf("%s", &waBinary.Node{Tag: "notification", Attrs: waBinary.Attrs{"type": "business"},
		Content: []waBinary.Node{{Tag: "wa_ad_account_nonce", Content: []byte("abc123")}}})
	select {
	case v := <-bizNonceCh:
		if v != "abc123" {
			t.Fatalf("nonce = %q", v)
		}
	default:
		t.Fatal("nonce não capturado")
	}
	// logger que não é o Recv não captura
	newNonceTapLogger(waLog.Noop).Sub("Send").Debugf("%s", &waBinary.Node{Tag: "notification",
		Content: []waBinary.Node{{Tag: "wa_ad_account_nonce", Content: []byte("x")}}})
	if len(bizNonceCh) != 0 {
		t.Fatal("Send não deveria capturar")
	}
}

func TestBizProductInfo(t *testing.T) {
	name, price, sale := "Fone", 199.9, 149.9
	pi := bizProductInput{Name: &name, Price: &price, SalePrice: &sale}.productInfo([]string{"https://mmg.whatsapp.net/x"}, true)
	if pi["price"] != "199900" || pi["sale_price"] != "149900" || pi["is_hidden"] != false || pi["compliance_category"] != "COUNTRY_ORIGIN_EXEMPT" {
		t.Fatalf("product_info inesperado: %+v", pi)
	}
	if _, ok := pi["media"]; !ok {
		t.Fatal("faltou media")
	}
}

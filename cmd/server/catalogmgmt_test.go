package main

import (
	"testing"

	waBinary "go.mau.fi/whatsmeow/binary"
)

func TestCatalogPrice(t *testing.T) {
	if got := priceToWA(199.90); got != "199900" {
		t.Fatalf("priceToWA(199.90) = %s", got)
	}
	if got := priceFromWA("199900"); got != 199.9 {
		t.Fatalf("priceFromWA = %v", got)
	}
}

func TestToProductNodeCreate(t *testing.T) {
	name, price := "Fone", 10.5
	n := toProductNode("", catalogProductInput{Name: &name, Price: &price}, []string{"https://mmg.whatsapp.net/x"}, true)
	if n.Tag != "product" || n.Attrs["is_hidden"] != "false" || n.Attrs["compliance_category"] != "COUNTRY_ORIGIN_EXEMPT" {
		t.Fatalf("attrs inesperados: %+v", n.Attrs)
	}
	if nodeText(&n, "name") != "Fone" || nodeText(&n, "price") != "10500" {
		t.Fatalf("conteúdo inesperado: %+v", n.Content)
	}
	media, ok := n.GetOptionalChildByTag("media")
	if !ok || len(media.GetChildrenByTag("image")) != 1 {
		t.Fatal("faltou <media><image>")
	}
	// editar não manda id vazio nem compliance
	e := toProductNode("123", catalogProductInput{Name: &name}, nil, false)
	if nodeText(&e, "id") != "123" || e.Attrs["compliance_category"] != nil {
		t.Fatalf("edit inesperado: %+v", e)
	}
	p := parseProductNode(&waBinary.Node{Tag: "product", Attrs: waBinary.Attrs{"is_hidden": "true"}, Content: []waBinary.Node{
		textNode("id", "9"), textNode("name", "X"), textNode("price", "1000"), textNode("currency", "BRL"),
	}})
	if p.ID != "9" || p.Price != 1 || !p.IsHidden {
		t.Fatalf("parse inesperado: %+v", p)
	}
}

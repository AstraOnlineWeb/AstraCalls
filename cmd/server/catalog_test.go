package main

import (
	"strings"
	"testing"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"google.golang.org/protobuf/proto"
)

func TestProductText(t *testing.T) {
	pm := &waE2E.ProductMessage{
		Product: &waE2E.ProductMessage_ProductSnapshot{
			Title:           proto.String("Camiseta Astra"),
			Description:     proto.String("100% algodão"),
			CurrencyCode:    proto.String("BRL"),
			PriceAmount1000: proto.Int64(49900), // 49.90
			ProductID:       proto.String("123456"),
			URL:             proto.String("https://loja.exemplo.com/p/123"),
		},
	}
	got := productText(pm)
	for _, want := range []string{"Camiseta Astra", "100% algodão", "BRL 49.90", "123456", "loja.exemplo.com"} {
		if !strings.Contains(got, want) {
			t.Fatalf("productText não contém %q. Saída:\n%s", want, got)
		}
	}
	if messageType(&waE2E.Message{ProductMessage: pm}) != "product" {
		t.Fatal("messageType deveria ser 'product'")
	}
}

func TestOrderText(t *testing.T) {
	om := &waE2E.OrderMessage{
		OrderID:           proto.String("ORD-99"),
		ItemCount:         proto.Int32(3),
		TotalAmount1000:   proto.Int64(150000), // 150.00
		TotalCurrencyCode: proto.String("BRL"),
	}
	got := orderText(om)
	for _, want := range []string{"Pedido recebido", "3 item", "BRL 150.00", "ORD-99"} {
		if !strings.Contains(got, want) {
			t.Fatalf("orderText não contém %q. Saída:\n%s", want, got)
		}
	}
	if messageType(&waE2E.Message{OrderMessage: om}) != "order" {
		t.Fatal("messageType deveria ser 'order'")
	}
}

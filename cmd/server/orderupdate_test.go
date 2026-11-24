package main

import (
	"encoding/json"
	"testing"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"google.golang.org/protobuf/proto"
)

// payload real da confirmação do cliente (24/11/2026, pedido 30000177439568054)
func TestOrderUpdateSummaryPayment(t *testing.T) {
	params := `{"reference_id":"4WSPYMWTJBQ","payment_method":"confirm","payment_timestamp":1795559510,"order":{"status":"payment_requested","items":[{"retailer_id":"28935733206126350","product_id":"28935733206126350","name":"Fone","amount":{"value":1999,"offset":10},"quantity":1}]},"total_amount":{"value":1999,"offset":10},"order_request_id":"30000177439568054","currency":"BRL"}`
	m := &waE2E.Message{InteractiveMessage: &waE2E.InteractiveMessage{
		InteractiveMessage: &waE2E.InteractiveMessage_NativeFlowMessage_{NativeFlowMessage: &waE2E.InteractiveMessage_NativeFlowMessage{
			Buttons: []*waE2E.InteractiveMessage_NativeFlowMessage_NativeFlowButton{{Name: proto.String("payment_method"), ButtonParamsJSON: proto.String(params)}},
		}},
	}}
	ou := orderUpdateSummary(m)
	b, _ := json.Marshal(ou)
	if ou["type"] != "payment" || ou["paymentMethod"] != "confirm" || ou["orderId"] != "30000177439568054" || ou["total"] != 199.9 {
		t.Fatalf("resumo inesperado: %s", b)
	}
	items := ou["items"].([]map[string]any)
	if len(items) != 1 || items[0]["amount"] != 199.9 || items[0]["productId"] != "28935733206126350" {
		t.Fatalf("itens inesperados: %s", b)
	}
}

func TestOrderChargeWithCatalogItems(t *testing.T) {
	msg, _, err := buildPixChargeMessage(pixChargeReq{
		OrderRequestID: "30000177439568054",
		Items:          []pixChargeItem{{Name: "Fone", Amount: 199.9, Quantity: 1, ProductID: "28935733206126350"}},
	}, "Loja")
	if err != nil {
		t.Fatal(err)
	}
	var p map[string]any
	_ = json.Unmarshal([]byte(msg.GetInteractiveMessage().GetNativeFlowMessage().GetButtons()[0].GetButtonParamsJSON()), &p)
	if p["order_request_id"] != "30000177439568054" {
		t.Fatalf("sem order_request_id: %v", p)
	}
	it := p["order"].(map[string]any)["items"].([]any)[0].(map[string]any)
	if it["product_id"] != "28935733206126350" || it["isCustomItem"] != false {
		t.Fatalf("item inesperado: %v", it)
	}
	ps := p["payment_settings"].([]any)[0].(map[string]any)
	if ps["type"] != "cards" {
		t.Fatalf("sem chave Pix deveria ir cards desligado: %v", ps)
	}
}

// confirmação real sem order_request_id/total: completa pela cobrança citada
func TestOrderUpdateFromQuoted(t *testing.T) {
	charge := `{"order_request_id":"30000177439568054","reference_id":"PIXMWVA0TA7","currency":"BRL","total_amount":{"value":19990,"offset":100},"order":{"status":"payment_requested","items":[]}}`
	confirm := `{"reference_id":"PIXMWVA0TA7","payment_method":"confirm","payment_timestamp":1795561515,"order":{"status":"payment_requested","items":[{"product_id":"28935733206126350","name":"Fone","amount":{"value":19990,"offset":100},"quantity":1}]}}`
	nf := func(name, params string) *waE2E.InteractiveMessage {
		return &waE2E.InteractiveMessage{InteractiveMessage: &waE2E.InteractiveMessage_NativeFlowMessage_{NativeFlowMessage: &waE2E.InteractiveMessage_NativeFlowMessage{
			Buttons: []*waE2E.InteractiveMessage_NativeFlowMessage_NativeFlowButton{{Name: proto.String(name), ButtonParamsJSON: proto.String(params)}},
		}}}
	}
	im := nf("payment_method", confirm)
	im.ContextInfo = &waE2E.ContextInfo{QuotedMessage: &waE2E.Message{InteractiveMessage: nf("review_and_pay", charge)}}
	ou := orderUpdateSummary(&waE2E.Message{InteractiveMessage: im})
	if ou["orderId"] != "30000177439568054" || ou["total"] != 199.9 || ou["type"] != "payment" {
		b, _ := json.Marshal(ou)
		t.Fatalf("não completou pela citada: %s", b)
	}
}

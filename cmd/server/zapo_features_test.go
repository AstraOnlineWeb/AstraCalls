package main

import (
	"encoding/json"
	"strings"
	"testing"

	"go.mau.fi/whatsmeow/proto/waAICommonDeprecated"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

func TestBizNativeFlowNodeNamed(t *testing.T) {
	for name, wantV := range map[string]bool{"mixed": true, "order_details": false, "catalog_message": false} {
		n := bizNativeFlowNodeNamed(name)
		inter := n.GetChildByTag("interactive")
		nf := inter.GetChildByTag("native_flow")
		if nf.Attrs["name"] != name {
			t.Fatalf("%s: name=%v", name, nf.Attrs["name"])
		}
		if _, hasV := nf.Attrs["v"]; hasV != wantV {
			t.Fatalf("%s: v presente=%v", name, hasV)
		}
	}
}

func TestPixKeyType(t *testing.T) {
	cases := map[string]string{
		"contato@empresa.com":                  "EMAIL",
		"+5561999998888":                       "PHONE",
		"123.456.789-09":                       "CPF",
		"12.345.678/0001-90":                   "CNPJ",
		"61999998888":                          "PHONE",
		"12345678909":                          "CPF",
		"123e4567-e89b-12d3-a456-426614174000": "EVP",
	}
	for k, want := range cases {
		if got := pixKeyType(k, ""); got != want {
			t.Errorf("%s: got %s want %s", k, got, want)
		}
	}
	if pixKeyType("61999998888", "cpf") != "CPF" {
		t.Error("keyType explícito deve vencer")
	}
}

func TestBuildPixCharge(t *testing.T) {
	msg, ref, err := buildPixChargeMessage(pixChargeReq{
		Key: "contato@empresa.com", MerchantName: "Loja", Reference: "PED-1",
		Items: []pixChargeItem{{Name: "Camisa", Amount: 49.9, Quantity: 2}, {Name: "Frete", Amount: 10}},
	}, "fallback")
	if err != nil || ref != "PED-1" {
		t.Fatal(err, ref)
	}
	btn := msg.GetInteractiveMessage().GetNativeFlowMessage().GetButtons()[0]
	if btn.GetName() != "review_and_pay" {
		t.Fatal(btn.GetName())
	}
	var p struct {
		Total struct{ Value, Offset int64 } `json:"total_amount"`
		Order struct {
			Status string `json:"status"`
			Items  []struct {
				IsCustomItem bool `json:"isCustomItem"`
				Quantity     int  `json:"quantity"`
			} `json:"items"`
		} `json:"order"`
		Settings []struct {
			Pix struct{ Key, KeyType string } `json:"pix_static_code"`
		} `json:"payment_settings"`
	}
	if err := json.Unmarshal([]byte(btn.GetButtonParamsJSON()), &p); err != nil {
		t.Fatal(err)
	}
	if p.Total.Value != 10980 || p.Total.Offset != 100 || p.Order.Status != "payment_requested" || len(p.Order.Items) != 2 || !p.Order.Items[0].IsCustomItem {
		t.Fatalf("params %+v", p)
	}
	if len(msg.GetMessageContextInfo().GetMessageSecret()) != 32 {
		t.Fatal("sem messageSecret")
	}
	if _, _, err := buildPixChargeMessage(pixChargeReq{Key: "x@y.z"}, ""); err == nil {
		t.Fatal("sem valor deveria falhar")
	}
	if _, _, err := buildPixChargeMessage(pixChargeReq{Key: "x@y.z", Amount: 5, Items: []pixChargeItem{{Name: "a", Amount: 4}}}, ""); err == nil {
		t.Fatal("amount != soma dos itens deveria falhar")
	}
	// a leitura de cobrança recebida reconhece review_and_pay
	if !hasPaymentButton(msg.GetInteractiveMessage()) || !strings.Contains(interactiveText(msg.GetInteractiveMessage()), "109.80") {
		t.Fatalf("leitura: %q", interactiveText(msg.GetInteractiveMessage()))
	}
}

func TestParseARGB(t *testing.T) {
	if v, _ := parseARGB("#233E4F", 0); v != 0xFF233E4F {
		t.Fatalf("%x", v)
	}
	if v, _ := parseARGB("80FFFFFF", 0); v != 0x80FFFFFF {
		t.Fatalf("%x", v)
	}
	if v, _ := parseARGB("", 7); v != 7 {
		t.Fatal(v)
	}
	if _, err := parseARGB("#12", 0); err == nil {
		t.Fatal("cor curta deveria falhar")
	}
}

func TestMetaAIText(t *testing.T) {
	rr := &waE2E.Message{RichResponseMessage: &waE2E.AIRichResponseMessage{
		Submessages: []*waAICommonDeprecated.AIRichResponseSubMessage{
			{MessageText: proto.String("Olá!")},
			{MessageText: proto.String("Posso ajudar.")},
		},
	}}
	if got := metaAIText(rr); got != "Olá!\n\nPosso ajudar." {
		t.Fatalf("%q", got)
	}
	edit := &waE2E.Message{ProtocolMessage: &waE2E.ProtocolMessage{EditedMessage: rr}}
	if metaAIText(edit) != "Olá!\n\nPosso ajudar." {
		t.Fatal("edição")
	}
	if metaAIText(&waE2E.Message{Conversation: proto.String("oi")}) != "oi" {
		t.Fatal("conversation")
	}
}

func TestIsMetaAIEvent(t *testing.T) {
	bot := &events.Message{Info: types.MessageInfo{MessageSource: types.MessageSource{Chat: types.NewMetaAIJID, Sender: types.NewMetaAIJID}}}
	old := &events.Message{Info: types.MessageInfo{MessageSource: types.MessageSource{Chat: types.MetaAIJID, Sender: types.MetaAIJID}}}
	user := types.NewJID("5561999998888", types.DefaultUserServer)
	normal := &events.Message{Info: types.MessageInfo{MessageSource: types.MessageSource{Chat: user, Sender: user}}}
	if !isMetaAIEvent(bot) || !isMetaAIEvent(old) || isMetaAIEvent(normal) {
		t.Fatal("classificação errada")
	}
}

func TestMetaAIChunkRouting(t *testing.T) {
	s := &Session{id: "t-meta"}
	st := metaAIFor(s.id)
	ch := make(chan metaAIChunk, 2)
	st.mu.Lock()
	st.waiters["P1"] = ch
	st.mu.Unlock()
	evt := &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{Chat: types.NewMetaAIJID, Sender: types.NewMetaAIJID},
			MsgMetaInfo:   types.MsgMetaInfo{TargetID: "P1"},
			MsgBotInfo:    types.MsgBotInfo{EditType: types.EditTypeLast},
		},
		Message: &waE2E.Message{Conversation: proto.String("resposta")},
	}
	s.handleMetaAIEvent(evt)
	c := <-ch
	if c.text != "resposta" || c.editType != types.EditTypeLast {
		t.Fatalf("%+v", c)
	}
}

func TestGroupStatusClassified(t *testing.T) {
	m := &waE2E.Message{GroupStatusMessageV2: &waE2E.FutureProofMessage{Message: &waE2E.Message{
		ExtendedTextMessage: &waE2E.ExtendedTextMessage{Text: proto.String("Promo")},
	}}}
	if messageType(m) != "group_status" || messageText(m) != "📢 Status do grupo: Promo" {
		t.Fatalf("%s %q", messageType(m), messageText(m))
	}
}

func TestMentionAllHelpers(t *testing.T) {
	for _, v := range []string{"all", "TODOS", " everyone ", "@todos"} {
		if !isMentionAll(v) {
			t.Errorf("isMentionAll(%q) = false", v)
		}
	}
	if isMentionAll("5561999999999") {
		t.Error("número não é mentionAll")
	}
	for txt, want := range map[string]bool{
		"@todos reunião às 10": true, "bom dia @All": true, "email a@todos.com": false, "todos": false,
	} {
		if got := mentionAllRe.MatchString(txt); got != want {
			t.Errorf("mentionAllRe(%q) = %v, quer %v", txt, got, want)
		}
	}
	if got := withMentionAll([]string{"1"}, true); len(got) != 2 || got[1] != "all" {
		t.Errorf("withMentionAll = %v", got)
	}
	if got := withMentionAll(nil, false); got != nil {
		t.Errorf("withMentionAll sem flag = %v", got)
	}
}

func TestMentionAllText(t *testing.T) {
	for in, want := range map[string]string{
		"@todos reunião": "@all reunião", "oi @Everyone": "oi @all", "@all ok": "@all ok", "a@todos.com": "a@todos.com",
	} {
		if got := mentionAllText(in); got != want {
			t.Errorf("mentionAllText(%q) = %q, quer %q", in, got, want)
		}
	}
	if !hasMentionAll([]string{"1", "all"}) || hasMentionAll([]string{"1"}) {
		t.Error("hasMentionAll")
	}
}

package main

import (
	"encoding/json"
	"os"
	"testing"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"google.golang.org/protobuf/encoding/protojson"
)

// Confere a mensagem montada contra a capturada de um envio real do celular.
func TestBuildNativeFormMessageMatchesPhone(t *testing.T) {
	data := map[string]any{"phone_number_visible": true, "full_name_visible": true, "email_visible": true}
	msg, token, err := buildNativeFormMessage("1615193897066865", "Compartilhe seus dados.", "", "contact_details", "", "", data)
	if err != nil {
		t.Fatal(err)
	}
	if len(token) != 36 {
		t.Fatalf("flow_token deveria ser UUID: %q", token)
	}
	nf := msg.GetInteractiveMessage().GetNativeFlowMessage()
	if nf.GetMessageVersion() != 3 || len(nf.GetButtons()) != 1 || nf.GetButtons()[0].GetName() != "galaxy_message" {
		t.Fatalf("estrutura errada: %+v", nf)
	}
	var p map[string]any
	if err := json.Unmarshal([]byte(nf.GetButtons()[0].GetButtonParamsJSON()), &p); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"flow_action": "navigate", "flow_message_version": "4", "flow_cta": "Preencher formulário",
		"form_type": "template", "flow_id": "1615193897066865", "flow_token": token}
	for k, v := range want {
		if p[k] != v {
			t.Errorf("%s = %v; quer %v", k, p[k], v)
		}
	}
	pl, _ := p["flow_action_payload"].(map[string]any)
	if pl["screen"] != "contact_details" || pl["data"] == nil {
		t.Errorf("payload errado: %v", pl)
	}
	if msg.GetMessageContextInfo().GetMessageSecret() == nil {
		t.Error("sem messageSecret")
	}
}

// Resposta real capturada de um formulário nativo preenchido no celular.
func TestParseNativeFormResponseReal(t *testing.T) {
	raw, err := os.ReadFile("testdata/nativeform_response.json")
	if err != nil {
		t.Fatal(err)
	}
	var msg waE2E.Message
	if err := protojson.Unmarshal(raw, &msg); err != nil {
		t.Fatal(err)
	}
	fr, ok := parseNativeFormResponse(msg.GetInteractiveResponseMessage())
	if !ok {
		t.Fatal("não reconheceu a resposta")
	}
	if fr.FlowID != "1615193897066865" || fr.FlowToken != "6BAC8E8A-88BF-4560-8B3C-46C1819AEA45" || fr.FlowName == "" {
		t.Fatalf("metadados errados: %+v", fr)
	}
	if fr.Answers["full_name"] != "Hh" || fr.Answers["phone_number"] != "966" || fr.Answers["email"] != "ggg@gnail.com" {
		t.Fatalf("respostas erradas: %v", fr.Answers)
	}
	want := "📋 Formulário respondido\nNome: Hh\nTelefone: 966\nE-mail: ggg@gnail.com"
	if got := messageText(&msg); got != want {
		t.Fatalf("texto = %q; quer %q", got, want)
	}
}

func TestGalaxyButtonParams(t *testing.T) {
	msg, token, err := buildNativeFormMessage("123", "Oi", "Cadastrar", "contact_details", "", "", map[string]any{"email_visible": true})
	if err != nil {
		t.Fatal(err)
	}
	p, body, ok := galaxyButtonParams(msg)
	if !ok || body != "Oi" || p["flow_id"] != "123" || p["flow_cta"] != "Cadastrar" || p["flow_token"] != token {
		t.Fatalf("leitura errada: %v %q %v", p, body, ok)
	}
	if _, _, ok := galaxyButtonParams(&waE2E.Message{}); ok {
		t.Fatal("mensagem sem formulário não deveria casar")
	}
}

package main

import (
	"encoding/json"
	"testing"
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

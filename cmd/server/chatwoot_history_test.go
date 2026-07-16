package main

import "testing"

func TestDirFromPrivate(t *testing.T) {
	if dirFromPrivate(true) != cwPrivate {
		t.Fatal("private=true deve virar cwPrivate")
	}
	if dirFromPrivate(false) != cwIncoming {
		t.Fatal("private=false deve virar cwIncoming")
	}
}

func TestApplyDir(t *testing.T) {
	cases := []struct {
		name        string
		dir         cwDir
		wantType    string
		wantPrivate bool
	}{
		{"incoming (recebida)", cwIncoming, "incoming", false},
		{"outgoing (enviada, histórico)", cwOutgoing, "outgoing", false},
		{"nota privada", cwPrivate, "outgoing", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			body := map[string]any{}
			c.dir.applyDir(body)
			if body["message_type"] != c.wantType {
				t.Fatalf("message_type = %v, quer %q", body["message_type"], c.wantType)
			}
			priv, _ := body["private"].(bool)
			if priv != c.wantPrivate {
				t.Fatalf("private = %v, quer %v", priv, c.wantPrivate)
			}
		})
	}
}

// Só cwOutgoing e cwPrivate podem estar juntas com source_id sem reenviar: a
// primeira porque o webhook ignora outgoing com source_id, a segunda por ser nota
// privada. A recebida (incoming) nunca reenvia. Garante que nenhuma direção do
// histórico produz uma outgoing SEM a marca private E SEM depender de source_id.
func TestHistoriaNaoProduzOutgoingDesprotegida(t *testing.T) {
	// enviada do histórico = outgoing; a proteção é o source_id (testado no webhook).
	body := map[string]any{}
	cwOutgoing.applyDir(body)
	if body["message_type"] != "outgoing" {
		t.Fatal("enviada do histórico deveria ser outgoing")
	}
	if p, _ := body["private"].(bool); p {
		t.Fatal("outgoing do histórico não deve ser privada (fica como bolha real)")
	}
}

// A guarda do webhook: só reenvia outgoing "de verdade" do agente. O caso
// crítico do histórico é a linha do source_id — uma outgoing importada volta pelo
// webhook e NÃO pode ser reenviada.
func TestShouldRelayWebhook(t *testing.T) {
	base := func(over map[string]any) map[string]any {
		m := map[string]any{"event": "message_created", "message_type": "outgoing"}
		for k, v := range over {
			m[k] = v
		}
		return m
	}
	cases := []struct {
		name string
		body map[string]any
		want bool
	}{
		{"agente escreveu (reenvia)", base(nil), true},
		{"mensagem de entrada", base(map[string]any{"message_type": "incoming"}), false},
		{"outro evento", base(map[string]any{"event": "conversation_updated"}), false},
		{"nota privada", base(map[string]any{"private": true}), false},
		{"histórico importado (source_id)", base(map[string]any{"source_id": "3EB0ABC"}), false},
		{"echo do aparelho (source_id)", base(map[string]any{"source_id": "XYZ"}), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := shouldRelayWebhook(c.body); got != c.want {
				t.Fatalf("shouldRelayWebhook = %v, quer %v", got, c.want)
			}
		})
	}
}

func TestContentAttrs(t *testing.T) {
	if contentAttrs("", 0) != nil {
		t.Fatal("sem campos deve devolver nil")
	}
	ca := contentAttrs("ABC123", 1700000000)
	if ca["in_reply_to_external_id"] != "ABC123" {
		t.Fatalf("in_reply_to ausente: %+v", ca)
	}
	if ca["external_created_at"] != int64(1700000000) {
		t.Fatalf("external_created_at ausente: %+v", ca)
	}
	only := contentAttrs("", 1700000000)
	if _, ok := only["in_reply_to_external_id"]; ok {
		t.Fatal("não deveria ter in_reply_to quando vazio")
	}
	if only["external_created_at"] != int64(1700000000) {
		t.Fatal("external_created_at deveria estar presente")
	}
}

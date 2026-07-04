package main

import (
	"encoding/json"
	"fmt"
	"strings"

	"go.mau.fi/whatsmeow/proto/waE2E"
)

// Cobrança Pix chega como interactiveMessage.nativeFlowMessage, botão
// "payment_info", com os dados num buttonParamsJSON (string JSON aninhada).

type pixButtonParams struct {
	ReferenceID string `json:"reference_id"`
	Currency    string `json:"currency"`
	TotalAmount struct {
		Value  int64 `json:"value"`
		Offset int64 `json:"offset"`
	} `json:"total_amount"`
	PaymentSettings []struct {
		Type          string `json:"type"`
		PixStaticCode struct {
			MerchantName string `json:"merchant_name"`
			Key          string `json:"key"`
			KeyType      string `json:"key_type"`
		} `json:"pix_static_code"`
	} `json:"payment_settings"`
}

var pixKeyTypeLabel = map[string]string{
	"PHONE": "telefone", "EMAIL": "email", "CPF": "CPF", "CNPJ": "CNPJ", "EVP": "chave aleatória",
}

// pixFromButtonParams formata a cobrança Pix a partir do buttonParamsJSON.
func pixFromButtonParams(paramsJSON string) string {
	var p pixButtonParams
	if json.Unmarshal([]byte(paramsJSON), &p) != nil {
		return ""
	}
	merchant, key, keyType := "", "", ""
	for _, ps := range p.PaymentSettings {
		if ps.PixStaticCode.Key != "" {
			merchant, key, keyType = ps.PixStaticCode.MerchantName, ps.PixStaticCode.Key, ps.PixStaticCode.KeyType
			break
		}
	}
	if key == "" {
		return ""
	}
	var b strings.Builder
	b.WriteString("💠 *Cobrança via Pix*")
	if merchant != "" {
		b.WriteString("\n" + merchant)
	}
	kt := pixKeyTypeLabel[keyType]
	if kt == "" {
		kt = strings.ToLower(keyType)
	}
	b.WriteString("\n🔑 Chave (" + kt + "): " + key)
	if p.TotalAmount.Value > 0 {
		off := p.TotalAmount.Offset
		if off == 0 {
			off = 100
		}
		b.WriteString(fmt.Sprintf("\n💰 %s %.2f", firstNonEmpty(p.Currency, "BRL"), float64(p.TotalAmount.Value)/float64(off)))
	}
	if p.ReferenceID != "" {
		b.WriteString("\n_Ref: " + p.ReferenceID + "_")
	}
	return b.String()
}

// hasPaymentButton diz se o nativeFlow tem um botão de pagamento (Pix).
func hasPaymentButton(im *waE2E.InteractiveMessage) bool {
	if nf := im.GetNativeFlowMessage(); nf != nil {
		for _, btn := range nf.GetButtons() {
			if strings.Contains(btn.GetName(), "payment") {
				return true
			}
		}
	}
	return false
}

// interactiveText formata um interactiveMessage (hoje: cobrança Pix; fallback =
// texto do corpo).
func interactiveText(im *waE2E.InteractiveMessage) string {
	if im == nil {
		return ""
	}
	if nf := im.GetNativeFlowMessage(); nf != nil {
		for _, btn := range nf.GetButtons() {
			if strings.Contains(btn.GetName(), "payment") {
				if t := pixFromButtonParams(btn.GetButtonParamsJSON()); t != "" {
					return t
				}
			}
		}
	}
	return im.GetBody().GetText()
}

// interactiveType classifica o interactiveMessage.
func interactiveType(im *waE2E.InteractiveMessage) string {
	if hasPaymentButton(im) {
		return "payment"
	}
	return "interactive"
}

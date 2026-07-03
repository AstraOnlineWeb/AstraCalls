package main

import (
	"fmt"
	"strings"

	"go.mau.fi/whatsmeow/proto/waE2E"
)

// Formatação de mensagens de Catálogo/Produto e Pedido do WhatsApp Business,
// para virarem texto legível no Chatwoot/webhook. O whatsmeow entrega esses
// eventos como ProductMessage (produto ou catálogo compartilhado) e OrderMessage
// (pedido feito a partir do catálogo).

// price1000 converte o preço em milésimos (padrão do WhatsApp) para "R$ 12.34".
func price1000(currency string, amount1000 int64) string {
	if currency == "" && amount1000 == 0 {
		return ""
	}
	return strings.TrimSpace(fmt.Sprintf("%s %.2f", currency, float64(amount1000)/1000))
}

// productText formata um ProductMessage (card de produto e/ou catálogo).
func productText(pm *waE2E.ProductMessage) string {
	if pm == nil {
		return ""
	}
	var b strings.Builder
	if p := pm.GetProduct(); p != nil {
		title := p.GetTitle()
		if title == "" {
			title = "Produto"
		}
		b.WriteString("🛍️ *" + title + "*")
		if d := p.GetDescription(); d != "" {
			b.WriteString("\n" + d)
		}
		if pr := price1000(p.GetCurrencyCode(), p.GetPriceAmount1000()); pr != "" {
			b.WriteString("\n💰 " + pr)
			if sp := p.GetSalePriceAmount1000(); sp > 0 {
				b.WriteString(" (promoção: " + price1000(p.GetCurrencyCode(), sp) + ")")
			}
		}
		if u := p.GetURL(); u != "" {
			b.WriteString("\n🔗 " + u)
		}
		if id := p.GetProductID(); id != "" {
			b.WriteString("\n_ID: " + id + "_")
		}
	}
	if c := pm.GetCatalog(); c != nil && (c.GetTitle() != "" || c.GetDescription() != "") {
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString("📖 *Catálogo")
		if t := c.GetTitle(); t != "" {
			b.WriteString(": " + t)
		}
		b.WriteString("*")
		if d := c.GetDescription(); d != "" {
			b.WriteString("\n" + d)
		}
	}
	if body := pm.GetBody(); body != "" {
		b.WriteString("\n" + body)
	}
	if footer := pm.GetFooter(); footer != "" {
		b.WriteString("\n_" + footer + "_")
	}
	return strings.TrimSpace(b.String())
}

// productImage devolve a imagem (ImageMessage) do produto ou do catálogo, ou nil.
func productImage(pm *waE2E.ProductMessage) *waE2E.ImageMessage {
	if pm == nil {
		return nil
	}
	if p := pm.GetProduct(); p != nil && p.GetProductImage() != nil {
		return p.GetProductImage()
	}
	if c := pm.GetCatalog(); c != nil && c.GetCatalogImage() != nil {
		return c.GetCatalogImage()
	}
	return nil
}

// orderText formata um OrderMessage (pedido feito pelo cliente).
func orderText(om *waE2E.OrderMessage) string {
	if om == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString("🧾 *Pedido recebido*")
	if t := om.GetOrderTitle(); t != "" {
		b.WriteString("\n" + t)
	}
	if n := om.GetItemCount(); n > 0 {
		b.WriteString(fmt.Sprintf("\n%d item(ns)", n))
	}
	if tot := price1000(om.GetTotalCurrencyCode(), om.GetTotalAmount1000()); tot != "" {
		b.WriteString("\nTotal: " + tot)
	}
	if msg := om.GetMessage(); msg != "" {
		b.WriteString("\n" + msg)
	}
	if id := om.GetOrderID(); id != "" {
		b.WriteString("\n_Pedido: " + id + "_")
	}
	return b.String()
}

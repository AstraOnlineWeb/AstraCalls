package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"google.golang.org/protobuf/proto"
)

// O card de produto NATIVO (ProductMessage do catálogo) é recurso da API oficial do
// WhatsApp Business e não pode ser enviado por cliente não-oficial (whatsmeow). O que
// funciona é mandar o produto como imagem (a foto) + legenda formatada — ou, sem foto,
// como texto. É o mesmo formato legível que já usamos no inbound (productText).

// productSendBody é o corpo do POST /messages/product.
type productSendBody struct {
	To          string    `json:"to"`
	Title       string    `json:"title"`
	Description string    `json:"description"`
	Price       flexFloat `json:"price"`
	Currency    string    `json:"currency"`  // ex.: "R$" (default) ou "BRL"
	SalePrice   flexFloat `json:"salePrice"` // preço promocional (opcional)
	URL         string    `json:"url"`
	ProductID   string    `json:"productId"`
	Footer      string    `json:"footer"`
	Base64      string    `json:"image"`    // foto do produto (base64) — opcional
	ImageURL    string    `json:"imageUrl"` // ou por URL — opcional
	Mimetype    string    `json:"mimetype"`
}

// productCaption monta a legenda/texto do produto no mesmo estilo do inbound.
func productCaption(b productSendBody) string {
	currency := strings.TrimSpace(b.Currency)
	if currency == "" {
		currency = "R$"
	}
	price := func(v float64) string { return strings.TrimSpace(fmt.Sprintf("%s %.2f", currency, v)) }

	title := strings.TrimSpace(b.Title)
	if title == "" {
		title = "Produto"
	}
	var sb strings.Builder
	sb.WriteString("🛍️ *" + title + "*")
	if d := strings.TrimSpace(b.Description); d != "" {
		sb.WriteString("\n" + d)
	}
	if b.Price.Float() > 0 {
		sb.WriteString("\n💰 " + price(b.Price.Float()))
		if b.SalePrice.Float() > 0 {
			sb.WriteString(" (promoção: " + price(b.SalePrice.Float()) + ")")
		}
	}
	if u := strings.TrimSpace(b.URL); u != "" {
		sb.WriteString("\n🔗 " + u)
	}
	if id := strings.TrimSpace(b.ProductID); id != "" {
		sb.WriteString("\n_ID: " + id + "_")
	}
	if f := strings.TrimSpace(b.Footer); f != "" {
		sb.WriteString("\n_" + f + "_")
	}
	return strings.TrimSpace(sb.String())
}

// handleSendProduct envia um produto como imagem+legenda (se houver foto) ou como
// texto. Corpo: productSendBody.
func (s *server) handleSendProduct(w http.ResponseWriter, r *http.Request) {
	sess := s.pairedSession(w, r.PathValue("sid"))
	if sess == nil {
		return
	}
	var b productSendBody
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid body"})
		return
	}
	if strings.TrimSpace(b.Title) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "title required"})
		return
	}
	caption := productCaption(b)

	// Sem foto → manda como texto (extendedText p/ preservar o link com preview).
	if strings.TrimSpace(b.Base64) == "" && strings.TrimSpace(b.ImageURL) == "" {
		s.send(sess, w, r, b.To, &waE2E.Message{ExtendedTextMessage: &waE2E.ExtendedTextMessage{
			Text: proto.String(caption),
		}})
		return
	}

	up, ok := s.uploadMedia(sess, w, r, b.Base64, b.ImageURL, whatsmeow.MediaImage)
	if !ok {
		return
	}
	mime := b.Mimetype
	if mime == "" {
		mime = "image/jpeg"
	}
	s.send(sess, w, r, b.To, &waE2E.Message{ImageMessage: &waE2E.ImageMessage{
		Caption: proto.String(caption), Mimetype: proto.String(mime),
		URL: &up.URL, DirectPath: &up.DirectPath, MediaKey: up.MediaKey,
		FileEncSHA256: up.FileEncSHA256, FileSHA256: up.FileSHA256, FileLength: proto.Uint64(up.FileLength),
	}})
}

// handleSendProductNative envia o CARD DE PRODUTO NATIVO (ProductMessage) referenciando
// um produto REAL do catálogo da conta. SÓ funciona se a conta conectada for WhatsApp
// Business E o `productId` existir no catálogo dela — o app do destinatário busca o
// produto em `businessJid` por esse ID. Com ID inexistente, o card não renderiza.
// Corpo: {to, productId, businessJid?, title?, description?, price?, salePrice?,
//         currency?, retailerId?, url?, image?/imageUrl?}
func (s *server) handleSendProductNative(w http.ResponseWriter, r *http.Request) {
	sess := s.pairedSession(w, r.PathValue("sid"))
	if sess == nil {
		return
	}
	var b struct {
		To          string    `json:"to"`
		ProductID   string    `json:"productId"`
		BusinessJID string    `json:"businessJid"`
		Title       string    `json:"title"`
		Description string    `json:"description"`
		Price       flexFloat `json:"price"`
		SalePrice   flexFloat `json:"salePrice"`
		Currency    string    `json:"currency"`
		RetailerID  string    `json:"retailerId"`
		URL         string    `json:"url"`
		Base64      string    `json:"image"`
		ImageURL    string    `json:"imageUrl"`
		Mimetype    string    `json:"mimetype"`
	}
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid body"})
		return
	}
	if strings.TrimSpace(b.ProductID) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "productId required"})
		return
	}
	// dono do catálogo: por padrão a própria conta conectada
	bizJid := strings.TrimSpace(b.BusinessJID)
	if bizJid == "" {
		if sess.client.Store.ID == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "not paired"})
			return
		}
		bizJid = sess.client.Store.ID.ToNonAD().String()
	}
	currency := strings.TrimSpace(b.Currency)
	if currency == "" {
		currency = "BRL"
	}

	snap := &waE2E.ProductMessage_ProductSnapshot{
		ProductID:       proto.String(strings.TrimSpace(b.ProductID)),
		Title:           proto.String(b.Title),
		Description:     proto.String(b.Description),
		CurrencyCode:    proto.String(currency),
		PriceAmount1000: proto.Int64(int64(b.Price.Float()*1000 + 0.5)),
		RetailerID:      proto.String(b.RetailerID),
		URL:             proto.String(b.URL),
	}
	if b.SalePrice.Float() > 0 {
		snap.SalePriceAmount1000 = proto.Int64(int64(b.SalePrice.Float()*1000 + 0.5))
	}
	// thumbnail do snapshot (opcional, mas melhora a prévia do card)
	if strings.TrimSpace(b.Base64) != "" || strings.TrimSpace(b.ImageURL) != "" {
		up, ok := s.uploadMedia(sess, w, r, b.Base64, b.ImageURL, whatsmeow.MediaImage)
		if !ok {
			return
		}
		mime := b.Mimetype
		if mime == "" {
			mime = "image/jpeg"
		}
		snap.ProductImage = &waE2E.ImageMessage{
			Mimetype: proto.String(mime),
			URL:      &up.URL, DirectPath: &up.DirectPath, MediaKey: up.MediaKey,
			FileEncSHA256: up.FileEncSHA256, FileSHA256: up.FileSHA256, FileLength: proto.Uint64(up.FileLength),
		}
		snap.ProductImageCount = proto.Uint32(1)
	}

	s.send(sess, w, r, b.To, &waE2E.Message{ProductMessage: &waE2E.ProductMessage{
		Product:          snap,
		BusinessOwnerJID: proto.String(bizJid),
	}})
}

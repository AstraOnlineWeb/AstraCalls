package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"google.golang.org/protobuf/proto"
)

// Dois jeitos de cobrar por Pix:
//   - padrão: o "Pix Copia e Cola" (BR Code no padrão EMV do Banco Central) como
//     texto — funciona em qualquer conta; o cliente cola no app do banco;
//   - style "charge": o CARTÃO DE COBRANÇA nativo do WhatsApp Business
//     (nativeFlow review_and_pay), o mesmo do "Cobrar" do app: referência, itens,
//     total e botão Pagar que abre o fluxo de pagamento. Só sai com o <biz>
//     native_flow name="order_details" (com "mixed" o celular não renderiza).

// emvField codifica um campo EMV: ID(2) + tamanho(2) + valor. O tamanho é em bytes.
func emvField(id, value string) string {
	return id + leftPad2(len(value)) + value
}

func leftPad2(n int) string {
	s := strconv.Itoa(n)
	if len(s) < 2 {
		return "0" + s
	}
	return s
}

// crc16CCITT calcula o CRC16-CCITT-FALSE (poly 0x1021, init 0xFFFF) exigido no
// campo 63 do BR Code, sobre a string já com "6304" no fim.
func crc16CCITT(s string) uint16 {
	crc := uint16(0xFFFF)
	for i := 0; i < len(s); i++ {
		crc ^= uint16(s[i]) << 8
		for j := 0; j < 8; j++ {
			if crc&0x8000 != 0 {
				crc = (crc << 1) ^ 0x1021
			} else {
				crc <<= 1
			}
		}
	}
	return crc
}

// asciiFold remove acentos e caracteres não-ASCII e coloca em maiúsculas — nome e
// cidade do BR Code precisam ser ASCII (bancos rejeitam/embaralham acento).
func asciiFold(s string) string {
	repl := map[rune]rune{
		'á': 'a', 'à': 'a', 'ã': 'a', 'â': 'a', 'ä': 'a',
		'é': 'e', 'ê': 'e', 'è': 'e', 'ë': 'e',
		'í': 'i', 'î': 'i', 'ì': 'i', 'ï': 'i',
		'ó': 'o', 'ô': 'o', 'õ': 'o', 'ò': 'o', 'ö': 'o',
		'ú': 'u', 'û': 'u', 'ù': 'u', 'ü': 'u', 'ç': 'c',
	}
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if m, ok := repl[r]; ok {
			r = m
		}
		if r < 128 {
			b.WriteRune(r)
		}
	}
	return strings.ToUpper(b.String())
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// buildPixBRCode monta o "Pix Copia e Cola" (BR Code estático). amount<=0 gera um
// código sem valor (o pagador digita). name/city têm limites do padrão (25/15).
func buildPixBRCode(key, name, city string, amount float64, txid string) string {
	name = truncate(asciiFold(strings.TrimSpace(name)), 25)
	if name == "" {
		name = "PIX"
	}
	city = truncate(asciiFold(strings.TrimSpace(city)), 15)
	if city == "" {
		city = "BRASIL"
	}
	txid = truncate(strings.TrimSpace(txid), 25)
	if txid == "" {
		txid = "***"
	}

	// Merchant Account Information (26): GUI do Pix + chave.
	mai := emvField("00", "br.gov.bcb.pix") + emvField("01", strings.TrimSpace(key))

	payload := emvField("00", "01") // Payload Format Indicator
	if amount > 0 {
		payload += emvField("01", "12") // point of initiation: uso único (valor fixo)
	}
	payload += emvField("26", mai)
	payload += emvField("52", "0000") // Merchant Category Code
	payload += emvField("53", "986")  // moeda: BRL
	if amount > 0 {
		payload += emvField("54", strconv.FormatFloat(amount, 'f', 2, 64))
	}
	payload += emvField("58", "BR")
	payload += emvField("59", name)
	payload += emvField("60", city)
	payload += emvField("62", emvField("05", txid)) // Additional Data: txid
	payload += "6304"                               // CRC (id 63, len 04) + valor a seguir

	crc := crc16CCITT(payload)
	hex := strconv.FormatInt(int64(crc), 16)
	for len(hex) < 4 {
		hex = "0" + hex
	}
	return payload + strings.ToUpper(hex)
}

// handleSendPix envia uma cobrança Pix como "copia e cola" (texto). Corpo:
//
//	{to, key, amount?, merchantName?, merchantCity?, txid?, description?, code?}
//
// Se "code" vier preenchido, usa esse BR Code pronto e ignora os demais campos de
// geração. A descrição (se houver) vai numa mensagem separada, e o código vai
// sozinho numa segunda mensagem — assim o cliente copia o código limpo.
func (s *server) handleSendPix(w http.ResponseWriter, r *http.Request) {
	sess := s.pairedSession(w, r.PathValue("sid"))
	if sess == nil {
		return
	}
	var b struct {
		To           string    `json:"to"`
		Key          string    `json:"key"`
		Amount       flexFloat `json:"amount"`
		MerchantName string    `json:"merchantName"`
		MerchantCity string    `json:"merchantCity"`
		TxID         string    `json:"txid"`
		Description  string    `json:"description"`
		Code         string    `json:"code"`
		// cobrança nativa (style=charge)
		Style   string          `json:"style"`
		KeyType string          `json:"keyType"`
		Footer  string          `json:"footer"`
		Items   []pixChargeItem `json:"items"`
	}
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid body"})
		return
	}
	if strings.EqualFold(strings.TrimSpace(b.Style), "charge") {
		s.sendPixCharge(sess, w, r, pixChargeReq{
			To: b.To, Key: b.Key, KeyType: b.KeyType, MerchantName: b.MerchantName,
			Amount: b.Amount.Float(), Reference: b.TxID, Body: b.Description, Footer: b.Footer, Items: b.Items,
		})
		return
	}
	code := strings.TrimSpace(b.Code)
	if code == "" {
		if strings.TrimSpace(b.Key) == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "key or code required"})
			return
		}
		code = buildPixBRCode(b.Key, b.MerchantName, b.MerchantCity, b.Amount.Float(), b.TxID)
	}
	jid, err := resolveRecipient(b.To)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	sendText := func(text string) (string, error) {
		msg := &waE2E.Message{Conversation: proto.String(text)}
		resp, err := sess.client.SendMessage(r.Context(), jid, msg)
		if err != nil {
			return "", err
		}
		sess.recordOutgoing(jid, resp.ID, resp.Timestamp.UnixMilli(), msg)
		return resp.ID, nil
	}

	ids := make([]string, 0, 2)
	if d := strings.TrimSpace(b.Description); d != "" {
		if id, err := sendText(d); err == nil {
			ids = append(ids, id)
		}
	}
	id, err := sendText(code)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	ids = append(ids, id)

	writeJSON(w, http.StatusOK, map[string]any{
		"id": id, "ids": ids, "to": jid.String(), "code": code,
	})
}

// ===== cobrança nativa (review_and_pay) =====

type pixChargeItem struct {
	Name     string    `json:"name"`
	Amount   flexFloat `json:"amount"` // valor unitário em reais
	Quantity int       `json:"quantity"`
}

type pixChargeReq struct {
	To, Key, KeyType, MerchantName string
	Amount                         float64
	Reference, Body, Footer        string
	Items                          []pixChargeItem
}

// pixKeyType deduz o tipo da chave Pix (o cartão mostra "Copiar chave <tipo>").
func pixKeyType(key, explicit string) string {
	if t := strings.ToUpper(strings.TrimSpace(explicit)); t != "" {
		if t == "RANDOM" || t == "ALEATORIA" {
			return "EVP"
		}
		return t
	}
	k := strings.TrimSpace(key)
	digits := digitsOnly(k)
	switch {
	case strings.Contains(k, "@"):
		return "EMAIL"
	case strings.HasPrefix(k, "+"):
		return "PHONE"
	case len(k) == 36 && strings.Count(k, "-") == 4:
		return "EVP"
	case len(digits) == 14:
		return "CNPJ"
	case len(digits) == 11 && strings.Contains(k, "."):
		return "CPF" // 123.456.789-00
	case len(digits) == 11 && digits[2] == '9':
		return "PHONE" // DDD + 9xxxx-xxxx (ambíguo com CPF: mande keyType se for CPF)
	case len(digits) == 11:
		return "CPF"
	case len(digits) >= 10:
		return "PHONE"
	}
	return "EVP"
}

func cents(v float64) int64 { return int64(v*100 + 0.5) }

// buildPixChargeMessage monta o cartão de cobrança. Valores em centavos com
// offset 100. Itens que não são produto do catálogo vão com isCustomItem: sem
// isso o WhatsApp Web procura o item no catálogo e o painel fica carregando.
func buildPixChargeMessage(c pixChargeReq, merchantFallback string) (*waE2E.Message, string, error) {
	type amt struct {
		Value  int64 `json:"value"`
		Offset int   `json:"offset"`
	}
	type item struct {
		Name          string `json:"name"`
		Amount        amt    `json:"amount"`
		Quantity      int    `json:"quantity"`
		IsCustomItem  bool   `json:"isCustomItem"`
		IsQuantitySet bool   `json:"isQuantitySet"`
	}
	items := make([]item, 0, len(c.Items))
	var total int64
	for _, it := range c.Items {
		q := it.Quantity
		if q <= 0 {
			q = 1
		}
		v := cents(it.Amount.Float())
		name := strings.TrimSpace(it.Name)
		if name == "" || v <= 0 {
			return nil, "", errors.New("cada item precisa de name e amount > 0")
		}
		items = append(items, item{Name: name, Amount: amt{v, 100}, Quantity: q, IsCustomItem: true, IsQuantitySet: true})
		total += v * int64(q)
	}
	if len(items) == 0 {
		total = cents(c.Amount)
		if total <= 0 {
			return nil, "", errors.New("amount > 0 obrigatório na cobrança")
		}
		items = append(items, item{Name: "Cobrança", Amount: amt{total, 100}, Quantity: 1, IsCustomItem: true, IsQuantitySet: true})
	} else if c.Amount > 0 && cents(c.Amount) != total {
		return nil, "", errors.New("amount diferente da soma dos itens")
	}
	merchant := strings.TrimSpace(c.MerchantName)
	if merchant == "" {
		merchant = merchantFallback
	}
	ref := strings.TrimSpace(c.Reference)
	if ref == "" {
		ref = "PIX" + strings.ToUpper(strconv.FormatInt(time.Now().UnixMilli(), 36))
	}
	params := map[string]any{
		"currency":     "BRL",
		"reference_id": ref,
		"type":         "physical-goods",
		"total_amount": amt{total, 100},
		"payment_settings": []any{map[string]any{
			"type": "pix_static_code",
			"pix_static_code": map[string]string{
				"merchant_name": merchant, "key": strings.TrimSpace(c.Key), "key_type": pixKeyType(c.Key, c.KeyType),
			},
		}},
		"order": map[string]any{
			"status": "payment_requested", "subtotal": amt{total, 100}, "order_type": "ORDER", "items": items,
		},
		"native_payment_methods": []any{},
		"share_payment_status":   false,
		"is_soft_deleted":        false,
	}
	pj, _ := json.Marshal(params)
	body := strings.TrimSpace(c.Body)
	if body == "" {
		body = "Sua cobrança está pronta."
	}
	im := &waE2E.InteractiveMessage{
		Body: &waE2E.InteractiveMessage_Body{Text: proto.String(body)},
		InteractiveMessage: &waE2E.InteractiveMessage_NativeFlowMessage_{
			NativeFlowMessage: &waE2E.InteractiveMessage_NativeFlowMessage{
				MessageVersion: proto.Int32(1),
				Buttons: []*waE2E.InteractiveMessage_NativeFlowMessage_NativeFlowButton{{
					Name: proto.String("review_and_pay"), ButtonParamsJSON: proto.String(string(pj)),
				}},
			},
		},
	}
	if f := strings.TrimSpace(c.Footer); f != "" {
		im.Footer = &waE2E.InteractiveMessage_Footer{Text: proto.String(f)}
	}
	return &waE2E.Message{
		InteractiveMessage: im,
		MessageContextInfo: &waE2E.MessageContextInfo{MessageSecret: newMessageSecret()},
	}, ref, nil
}

func (s *server) sendPixCharge(sess *Session, w http.ResponseWriter, r *http.Request, c pixChargeReq) {
	if strings.TrimSpace(c.Key) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "key obrigatória na cobrança"})
		return
	}
	jid, err := resolveRecipient(c.To)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	msg, _, err := buildPixChargeMessage(c, sess.name)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	s.sendNativeFlowNamed(sess, w, r, jid, msg, "order_details")
}

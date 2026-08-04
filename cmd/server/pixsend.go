package main

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"google.golang.org/protobuf/proto"
)

// O WhatsApp bloqueia o envio de cobrança Pix NATIVA (interactiveMessage/nativeFlow)
// por clientes não-oficiais — isso é recurso da API oficial do WhatsApp Business.
// A forma que funciona 100% é mandar o "Pix Copia e Cola" (BR Code no padrão EMV do
// Banco Central) como texto: o cliente cola no app do banco e paga. Este arquivo
// monta esse BR Code e o envia.

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
	}
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid body"})
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

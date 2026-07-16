package main

import (
	"context"
	"sort"
	"time"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/proto/waHistorySync"
	"go.mau.fi/whatsmeow/types"
)

// importHistorySync processa um chunk do HistorySync (conversas antigas que o
// WhatsApp envia ao parear) e importa as conversas 1:1 para o Chatwoot, na ordem
// cronológica. As mensagens entram com a DIREÇÃO real (recebidas como incoming,
// enviadas como outgoing) — o source_id = ID da msg do WhatsApp impede o reenvio
// (o webhook ignora outgoing com source_id). A data original é prefixada no texto,
// porque o Chatwoot carimba tudo com a hora da importação.
func (s *Session) importHistorySync(data *waHistorySync.HistorySync) {
	if data == nil {
		return
	}
	cfg := s.getChatwoot()
	if !cfg.valid() || !cfg.ImportHistory {
		return
	}
	days := cfg.ImportHistoryDays
	if days <= 0 {
		days = importHistoryDefaultDays
	}
	if days > importHistoryMaxDays {
		days = importHistoryMaxDays
	}
	cutoff := time.Now().Add(-time.Duration(days) * 24 * time.Hour).Unix()

	// serializa os chunks do HistorySync (evita criar contato/conversa duplicados)
	s.importMu.Lock()
	defer s.importMu.Unlock()

	total := 0
	for _, conv := range data.GetConversations() {
		total += s.importConversation(cfg, conv, cutoff)
	}
	if total > 0 {
		s.log.Info("chatwoot: histórico importado", "mensagens", total, "chunk", data.GetChunkOrder())
	}
}

// importConversation importa uma conversa 1:1. Devolve quantas mensagens postou.
func (s *Session) importConversation(cfg ChatwootConfig, conv *waHistorySync.Conversation, cutoff int64) int {
	jid, err := types.ParseJID(conv.GetID())
	if err != nil {
		return 0
	}
	// só conversas 1:1 nesta versão (grupos/canais têm 1 contato por conversa e
	// exigem prefixo de autor por mensagem — fica para depois).
	if jid.Server != types.DefaultUserServer && jid.Server != types.HiddenUserServer {
		return 0
	}

	// junta as mensagens válidas (com conteúdo e dentro da janela), mais antigas
	// primeiro. O HistorySync costuma vir mais recente primeiro.
	msgs := conv.GetMessages()
	type row struct {
		msg    *waE2E.Message
		id     string
		ts     uint64
		fromMe bool
	}
	rows := make([]row, 0, len(msgs))
	for _, hm := range msgs {
		wmi := hm.GetMessage()
		if wmi == nil {
			continue
		}
		ts := wmi.GetMessageTimestamp()
		if int64(ts) < cutoff {
			continue
		}
		msg := wmi.GetMessage()
		if msg == nil {
			continue
		}
		if inner, viewOnce := unwrapViewOnce(msg); viewOnce {
			msg = inner
		}
		// só mensagens com conteúdo de fato (texto ou mídia)
		if messageText(msg) == "" && downloadableOf(msg) == nil {
			continue
		}
		rows = append(rows, row{msg: msg, id: wmi.GetKey().GetID(), ts: ts, fromMe: wmi.GetKey().GetFromMe()})
	}
	if len(rows) == 0 {
		return 0
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].ts < rows[j].ts })
	// teto por conversa: mantém as mais recentes
	if len(rows) > importHistoryMaxPerChat {
		rows = rows[len(rows)-importHistoryMaxPerChat:]
	}

	// contato + conversa (uma vez por chat)
	phone := s.realPhone(jid)
	if phone == "" {
		phone = jid.User
	}
	chatID := phone + "@" + types.DefaultUserServer
	name := conv.GetName()
	if name == "" {
		name = phone
	}
	avatar := ""
	if pp, perr := s.client.GetProfilePictureInfo(context.Background(), jid, nil); perr == nil && pp != nil {
		avatar = pp.URL
	}
	contactID, sourceID, err := cfg.ensureContact(chatID, phone, name, avatar)
	if err != nil {
		s.log.Error("chatwoot: histórico — ensure contact falhou", "err", err)
		return 0
	}
	convID, err := cfg.ensureConversation(contactID, sourceID)
	if err != nil {
		s.log.Error("chatwoot: histórico — ensure conversation falhou", "err", err)
		return 0
	}

	posted := 0
	for _, r := range rows {
		// dedup entre chunks/reconexões (enquanto o processo vive)
		if _, loaded := s.importedMsgIDs.LoadOrStore(r.id, struct{}{}); loaded {
			continue
		}
		if s.importMessage(cfg, convID, r.msg, r.id, r.ts, r.fromMe) {
			posted++
		}
		// respeita o rate-limit do Chatwoot (importações grandes)
		time.Sleep(60 * time.Millisecond)
	}
	return posted
}

// importMessage posta uma mensagem do histórico no Chatwoot, com a direção real e
// a data original prefixada. Devolve true se postou.
func (s *Session) importMessage(cfg ChatwootConfig, convID int, msg *waE2E.Message, msgID string, ts uint64, fromMe bool) bool {
	// direção real: recebida à esquerda, enviada à direita. source_id = msgID
	// impede o reenvio (o webhook ignora outgoing com source_id).
	dir := cwIncoming
	if fromMe {
		dir = cwOutgoing
	}
	prefix := time.Unix(int64(ts), 0).Format("🕘 02/01/2006 15:04\n")
	text := messageText(msg)
	// mídia: baixa do WhatsApp e sobe como anexo
	if dl := downloadableOf(msg); dl != nil {
		data, derr := s.client.Download(context.Background(), dl)
		if derr == nil && len(data) > 0 {
			fname, mime := mediaMeta(msg)
			if err := cfg.postAttachment(convID, prefix+text, fname, mime, data, dir, msgID, "", int64(ts)); err != nil {
				s.log.Error("chatwoot: histórico — anexo falhou", "err", err)
				return false
			}
			return true
		}
	}
	if text == "" {
		return false
	}
	if err := cfg.postText(convID, prefix+text, dir, msgID, "", int64(ts)); err != nil {
		s.log.Error("chatwoot: histórico — texto falhou", "err", err)
		return false
	}
	return true
}

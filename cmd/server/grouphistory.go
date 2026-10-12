package main

import (
	"bytes"
	"compress/zlib"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	waBinary "go.mau.fi/whatsmeow/binary"
	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/proto/waGroupHistory"
	"go.mau.fi/whatsmeow/proto/waWeb"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/util/cbcutil"
	"go.mau.fi/whatsmeow/util/hkdfutil"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// =============================================================================
// Compartilhar histórico do grupo com quem entrou ("Group Message History")
// =============================================================================
//
// Recurso novo do WhatsApp: ao adicionar alguém, quem adiciona pode mandar as
// últimas mensagens do grupo (25/50/75/100) para o membro novo. O whatsmeow não
// implementa o envio (TODO no próprio CreateGroup); portado de vinikjkkj/zapo
// (MIT): shareGroupHistory + publishGroupDirectMessage.
//
// Protocolo:
//  1. As mensagens viram um GroupHistory{messages: []WebMessageInfo} (proto),
//     comprimido em zlib, criptografado como mídia com a chave HKDF "Group
//     History" e enviado ao CDN em /mms/group-history.
//  2. Uma mensagem messageHistoryBundle (mídia, mediatype=group_history) vai
//     para o JID do GRUPO, mas criptografada aparelho a aparelho (pkmsg/msg)
//     SÓ para os destinatários + nossos outros aparelhos — não pela sender key,
//     senão todos do grupo receberiam o pacote.
//  3. Uma messageHistoryNotice vai para o grupo todo (sender key normal) para
//     todos verem que o histórico foi compartilhado.
//
// As mensagens saem do nosso banco (tabela messages): só entra o que esta
// sessão viu chegar/enviou.

const (
	groupHistoryMediaInfo  = "Group History"
	groupHistoryMMSPath    = "group-history"
	groupHistoryMimetype   = "application/protobuf"
	groupHistoryEncType    = "group_history"
	groupHistoryDefaultCnt = 50
	groupHistoryMaxCnt     = 100
	waWebOrigin            = "https://web.whatsapp.com"
)

type groupHistoryResult struct {
	BundleMessageID     string   `json:"bundleMessageId"`
	NoticeMessageID     string   `json:"noticeMessageId,omitempty"`
	MessagesCount       int      `json:"messagesCount"`
	HistoryReceivers    []string `json:"historyReceivers"`
	NonHistoryReceivers []string `json:"nonHistoryReceivers"`
	Devices             int      `json:"devices"`
}

// errGroupHistoryInput marca erros de entrada (viram 400 no handler).
type errGroupHistoryInput struct{ msg string }

func (e errGroupHistoryInput) Error() string { return e.msg }

// matchGroupParticipant acha o participante do grupo correspondente a um número
// ou JID informado (PN, LID ou JID completo; tolera o 9º dígito BR).
func matchGroupParticipant(parts []types.GroupParticipant, in string) (types.GroupParticipant, bool) {
	in = strings.TrimSpace(in)
	user := in
	if i := strings.IndexByte(user, '@'); i >= 0 {
		user = user[:i]
	}
	if i := strings.IndexByte(user, ':'); i >= 0 {
		user = user[:i]
	}
	user = strings.TrimPrefix(user, "+")
	if !strings.Contains(in, "@lid") {
		user = digitsOnly(user)
	}
	if user == "" {
		return types.GroupParticipant{}, false
	}
	cands := []string{user}
	if v := brNinthDigitVariant(user); v != "" && v != user {
		cands = append(cands, v)
	}
	for _, p := range parts {
		for _, c := range cands {
			if p.JID.User == c || p.PhoneNumber.User == c || p.LID.User == c {
				return p, true
			}
		}
	}
	return types.GroupParticipant{}, false
}

// groupHistoryAudience divide os membros entre quem recebe o histórico e quem
// não recebe, no modo de endereçamento do grupo (LID ou PN). Erro se algum
// pedido não for membro, se pedir a própria conta ou se não sobrar ninguém.
func groupHistoryAudience(info *types.GroupInfo, requested []string, own types.JID) (receivers, others []types.JID, err error) {
	want := map[types.JID]bool{}
	var unknown []string
	for _, r := range requested {
		p, ok := matchGroupParticipant(info.Participants, r)
		if !ok {
			unknown = append(unknown, r)
			continue
		}
		if p.JID.User == own.User {
			return nil, nil, errGroupHistoryInput{"não dá para compartilhar o histórico com a própria conta"}
		}
		want[p.JID.ToNonAD()] = true
	}
	if len(unknown) > 0 {
		return nil, nil, errGroupHistoryInput{"não são membros do grupo (adicione antes): " + strings.Join(unknown, ", ")}
	}
	for _, p := range info.Participants {
		j := p.JID.ToNonAD()
		switch {
		case want[j]:
			receivers = append(receivers, j)
		case j.User == own.User:
		default:
			others = append(others, j)
		}
	}
	if len(receivers) == 0 {
		return nil, nil, errGroupHistoryInput{"nenhum destinatário"}
	}
	return receivers, others, nil
}

// ownIdentityFor devolve nosso JID no modo de endereçamento do grupo.
func (s *Session) ownIdentityFor(mode types.AddressingMode) types.JID {
	if mode == types.AddressingModeLID && !s.client.Store.LID.IsEmpty() {
		return s.client.Store.LID.ToNonAD()
	}
	if s.client.Store.ID != nil {
		return s.client.Store.ID.ToNonAD()
	}
	return types.EmptyJID
}

// loadGroupHistoryMessages lê as últimas `limit` mensagens do grupo no banco e
// as remonta como WebMessageInfo (ordem cronológica).
func (s *Session) loadGroupHistoryMessages(ctx context.Context, gid types.JID, limit int, own types.JID) ([]*waWeb.WebMessageInfo, error) {
	if s.mgr.store == nil {
		return nil, errGroupHistoryInput{"banco de mensagens desligado: não há histórico para compartilhar"}
	}
	rows, err := s.mgr.store.listMessages(ctx, s.id, gid.String(), limit, 0, true)
	if err != nil {
		return nil, err
	}
	out := make([]*waWeb.WebMessageInfo, 0, len(rows))
	for _, m := range rows {
		if len(m.Raw) == 0 || m.MsgID == "" {
			continue
		}
		var msg waE2E.Message
		if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(m.Raw, &msg); err != nil {
			continue
		}
		participant := m.SenderJID
		if m.FromMe && !own.IsEmpty() {
			participant = own.String()
		}
		out = append(out, &waWeb.WebMessageInfo{
			Key: &waCommon.MessageKey{
				RemoteJID:   proto.String(gid.String()),
				FromMe:      proto.Bool(m.FromMe),
				ID:          proto.String(m.MsgID),
				Participant: proto.String(participant),
			},
			Message:          &msg,
			MessageTimestamp: proto.Uint64(uint64(m.Timestamp / 1000)),
		})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].GetMessageTimestamp() < out[j].GetMessageTimestamp() })
	return out, nil
}

// encodeGroupHistoryBundle serializa e comprime (zlib) o pacote.
func encodeGroupHistoryBundle(msgs []*waWeb.WebMessageInfo) ([]byte, error) {
	raw, err := proto.Marshal(&waGroupHistory.GroupHistory{Messages: msgs})
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	zw := zlib.NewWriter(&buf)
	if _, err := zw.Write(raw); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// decodeGroupHistoryBundle é o inverso (usado em teste e diagnóstico).
func decodeGroupHistoryBundle(blob []byte) (*waGroupHistory.GroupHistory, error) {
	zr, err := zlib.NewReader(bytes.NewReader(blob))
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(zr); err != nil {
		return nil, err
	}
	var gh waGroupHistory.GroupHistory
	return &gh, proto.Unmarshal(buf.Bytes(), &gh)
}

type encryptedMedia struct {
	MediaKey, FileSHA256, FileEncSHA256, Upload []byte
	FileLength                                  uint64
}

// encryptGroupHistoryMedia criptografa como mídia do WhatsApp (AES-CBC + HMAC
// truncado em 10 bytes), com a chave derivada por HKDF "Group History".
func encryptGroupHistoryMedia(plaintext []byte) (*encryptedMedia, error) {
	mediaKey := make([]byte, 32)
	if _, err := rand.Read(mediaKey); err != nil {
		return nil, err
	}
	exp := hkdfutil.SHA256(mediaKey, nil, []byte(groupHistoryMediaInfo), 112)
	iv, cipherKey, macKey := exp[:16], exp[16:48], exp[48:80]
	ct, err := cbcutil.Encrypt(cipherKey, iv, plaintext)
	if err != nil {
		return nil, err
	}
	h := hmac.New(sha256.New, macKey)
	h.Write(iv)
	h.Write(ct)
	upload := append(ct, h.Sum(nil)[:10]...)
	fileSHA := sha256.Sum256(plaintext)
	encSHA := sha256.Sum256(upload)
	return &encryptedMedia{
		MediaKey: mediaKey, FileSHA256: fileSHA[:], FileEncSHA256: encSHA[:],
		Upload: upload, FileLength: uint64(len(plaintext)),
	}, nil
}

// uploadGroupHistoryMedia sobe o blob em /mms/group-history (o whatsmeow não
// conhece esse tipo de mídia, então o upload é feito aqui com o media conn dele).
func (s *Session) uploadGroupHistoryMedia(ctx context.Context, em *encryptedMedia) (directPath string, err error) {
	mc, err := s.client.DangerousInternals().RefreshMediaConn(ctx, false)
	if err != nil {
		return "", fmt.Errorf("media conn: %w", err)
	}
	if len(mc.Hosts) == 0 {
		return "", errors.New("media conn sem hosts")
	}
	token := base64.URLEncoding.EncodeToString(em.FileEncSHA256)
	u := url.URL{
		Scheme:   "https",
		Host:     mc.Hosts[0].Hostname,
		Path:     "/mms/" + groupHistoryMMSPath + "/" + token,
		RawQuery: url.Values{"auth": {mc.Auth}, "token": {token}}.Encode(),
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(em.Upload))
	if err != nil {
		return "", err
	}
	req.ContentLength = int64(len(em.Upload))
	req.Header.Set("Origin", waWebOrigin)
	req.Header.Set("Referer", waWebOrigin+"/")
	resp, err := (&http.Client{Timeout: 60 * time.Second}).Do(req)
	if err != nil {
		return "", fmt.Errorf("upload: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("upload falhou: HTTP %d", resp.StatusCode)
	}
	var out struct {
		DirectPath string `json:"direct_path"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", fmt.Errorf("resposta do upload: %w", err)
	}
	if out.DirectPath == "" {
		return "", errors.New("upload sem direct_path")
	}
	return out.DirectPath, nil
}

func jidStrings(js []types.JID) []string {
	out := make([]string, len(js))
	for i, j := range js {
		out[i] = j.String()
	}
	return out
}

// shareGroupHistory compartilha as últimas `count` mensagens do grupo com os
// membros informados (números/JIDs que JÁ estão no grupo).
func (s *Session) shareGroupHistory(ctx context.Context, gid types.JID, to []string, count int) (*groupHistoryResult, error) {
	if len(to) == 0 {
		return nil, errGroupHistoryInput{"participants obrigatório"}
	}
	if count <= 0 {
		count = groupHistoryDefaultCnt
	}
	if count > groupHistoryMaxCnt {
		count = groupHistoryMaxCnt
	}
	info, err := s.client.GetGroupInfo(ctx, gid)
	if err != nil {
		return nil, fmt.Errorf("info do grupo: %w", err)
	}
	mode := info.AddressingMode
	if mode == "" {
		mode = types.AddressingModePN
	}
	own := s.ownIdentityFor(mode)
	receivers, others, err := groupHistoryAudience(info, to, own)
	if err != nil {
		return nil, err
	}
	msgs, err := s.loadGroupHistoryMessages(ctx, gid, count, own)
	if err != nil {
		return nil, err
	}
	if len(msgs) == 0 {
		return nil, errGroupHistoryInput{"nenhuma mensagem deste grupo no banco do AstraCalls para compartilhar"}
	}
	blob, err := encodeGroupHistoryBundle(msgs)
	if err != nil {
		return nil, fmt.Errorf("montar pacote: %w", err)
	}
	em, err := encryptGroupHistoryMedia(blob)
	if err != nil {
		return nil, fmt.Errorf("criptografar pacote: %w", err)
	}
	directPath, err := s.uploadGroupHistoryMedia(ctx, em)
	if err != nil {
		return nil, err
	}

	oldest := int64(msgs[0].GetMessageTimestamp())
	meta := &waE2E.MessageHistoryMetadata{
		HistoryReceivers:               jidStrings(receivers),
		NonHistoryReceivers:            jidStrings(others),
		MessageCount:                   proto.Int64(int64(len(msgs))),
		OldestMessageTimestampInWindow: proto.Int64(oldest),
		OldestMessageTimestampInBundle: proto.Int64(oldest),
	}
	secret := make([]byte, 32)
	_, _ = rand.Read(secret)
	bundle := &waE2E.Message{
		MessageHistoryBundle: &waE2E.MessageHistoryBundle{
			Mimetype:               proto.String(groupHistoryMimetype),
			FileSHA256:             em.FileSHA256,
			MediaKey:               em.MediaKey,
			FileEncSHA256:          em.FileEncSHA256,
			DirectPath:             proto.String(directPath),
			MediaKeyTimestamp:      proto.Int64(time.Now().Unix()),
			MessageHistoryMetadata: meta,
		},
		MessageContextInfo: &waE2E.MessageContextInfo{MessageSecret: secret},
	}
	bundleID, devices, err := s.sendGroupDirect(ctx, gid, mode, append(append([]types.JID{}, receivers...), own), bundle, groupHistoryEncType)
	if err != nil {
		return nil, fmt.Errorf("enviar pacote: %w", err)
	}
	res := &groupHistoryResult{
		BundleMessageID: bundleID, MessagesCount: len(msgs), Devices: devices,
		HistoryReceivers: jidStrings(receivers), NonHistoryReceivers: jidStrings(others),
	}
	// aviso para o grupo todo ("X compartilhou o histórico com Y"). Falha aqui não
	// desfaz o envio do pacote — só é registrada.
	if r, nerr := s.client.SendMessage(ctx, gid, &waE2E.Message{
		MessageHistoryNotice: &waE2E.MessageHistoryNotice{MessageHistoryMetadata: meta},
	}); nerr != nil {
		s.log.Warn("group history: aviso no grupo falhou (pacote já enviado)", "group", gid.String(), "err", nerr)
	} else {
		res.NoticeMessageID = r.ID
	}
	s.log.Info("group history compartilhado", "group", gid.String(), "id", bundleID,
		"messages", len(msgs), "receivers", len(receivers), "devices", devices)
	return res, nil
}

// sendGroupDirect envia uma mensagem para o JID do grupo criptografada aparelho
// a aparelho só para `users` (em vez da sender key do grupo). É o caminho que o
// WhatsApp usa para o pacote de histórico.
func (s *Session) sendGroupDirect(ctx context.Context, gid types.JID, mode types.AddressingMode, users []types.JID, msg *waE2E.Message, mediatype string) (string, int, error) {
	devices, err := s.client.GetUserDevicesContext(ctx, users)
	if err != nil {
		return "", 0, fmt.Errorf("aparelhos dos destinatários: %w", err)
	}
	if len(devices) == 0 {
		return "", 0, errors.New("nenhum aparelho de destino")
	}
	plaintext, err := proto.Marshal(msg)
	if err != nil {
		return "", 0, err
	}
	id := s.client.GenerateMessageID()
	in := s.client.DangerousInternals()
	encAttrs := waBinary.Attrs{}
	if mediatype != "" {
		encAttrs["mediatype"] = mediatype
	}
	parts, includeIdentity, err := in.EncryptMessageForDevices(ctx, devices, id, plaintext, nil, encAttrs)
	if err != nil {
		return "", 0, fmt.Errorf("criptografar: %w", err)
	}
	if len(parts) == 0 {
		return "", 0, errors.New("nenhuma sessão Signal com os aparelhos de destino")
	}
	content := []waBinary.Node{{Tag: "participants", Content: parts}}
	if includeIdentity {
		content = append(content, in.MakeDeviceIdentityNode())
	}
	node := waBinary.Node{
		Tag: "message",
		Attrs: waBinary.Attrs{
			"id": id, "to": gid, "type": "media", "addressing_mode": string(mode),
		},
		Content: content,
	}
	if err := in.SendNode(ctx, node); err != nil {
		return "", 0, err
	}
	return id, len(parts), nil
}

// handleGroupHistoryShare atende POST /api/sessions/{sid}/groups/{gid}/history/share
// Body: {"participants": ["5511999999999"], "count": 50}
func (s *server) handleGroupHistoryShare(w http.ResponseWriter, r *http.Request) {
	sess, gid, ok := s.group(w, r)
	if !ok {
		return
	}
	var b struct {
		Participants []string   `json:"participants"`
		Count        *flexFloat `json:"count"`
	}
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil || len(b.Participants) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "participants required"})
		return
	}
	count := 0
	if b.Count != nil {
		count = int(*b.Count)
	}
	res, err := sess.shareGroupHistory(r.Context(), gid, b.Participants, count)
	if err != nil {
		var ie errGroupHistoryInput
		if errors.As(err, &ie) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "result": res})
}

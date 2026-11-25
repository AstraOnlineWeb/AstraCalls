package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	_ "embed"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"time"

	"go.mau.fi/whatsmeow"
	waBinary "go.mau.fi/whatsmeow/binary"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"
)

// Figurinha com som (sound sticker), porte do zapo-interactive-examples.
//
// No fio é um stickerMessage cujo campo 26 (audioMessage) leva o clipe Opus,
// embrulhado em audioStickerMessage (campo 134), com messageSecret só na
// mensagem de fora. O WebP NÃO carrega o som: ele leva no EXIF o SHA-256
// (base64) do áudio enviado, e é assim que o app pareia os dois. A stanza tem
// de sair type="media" com <enc mediatype="sticker">, e o whatsmeow não
// reconhece esse envelope (sairia type="text" sem mediatype), por isso o envio
// é montado à mão (sendTypedDM).

//go:embed soundsticker_default.webp
var soundStickerDefaultWebP []byte

//go:embed soundsticker_default.ogg
var soundStickerDefaultOgg []byte

const soundStickerSize = 512

type webpChunk struct {
	id      string
	payload []byte
}

func readWebPChunks(webp []byte) ([]webpChunk, error) {
	if len(webp) < 12 || string(webp[0:4]) != "RIFF" || string(webp[8:12]) != "WEBP" {
		return nil, errors.New("figurinha não é WebP")
	}
	var out []webpChunk
	for at := 12; at+8 <= len(webp); {
		size := int(binary.LittleEndian.Uint32(webp[at+4 : at+8]))
		end := at + 8 + size
		if end > len(webp) {
			return nil, errors.New("WebP truncado")
		}
		out = append(out, webpChunk{id: string(webp[at : at+4]), payload: append([]byte(nil), webp[at+8:end]...)})
		at = end + size%2
	}
	return out, nil
}

func writeWebP(chunks []webpChunk) []byte {
	var body bytes.Buffer
	body.WriteString("WEBP")
	for _, c := range chunks {
		var h [8]byte
		copy(h[:4], c.id)
		binary.LittleEndian.PutUint32(h[4:], uint32(len(c.payload)))
		body.Write(h[:])
		body.Write(c.payload)
		if len(c.payload)%2 == 1 {
			body.WriteByte(0)
		}
	}
	out := make([]byte, 8, 8+body.Len())
	copy(out, "RIFF")
	binary.LittleEndian.PutUint32(out[4:], uint32(body.Len()))
	return append(out, body.Bytes()...)
}

// webpIsAnimated: WebP estendido (VP8X) com a flag de animação.
func webpIsAnimated(webp []byte) bool {
	chunks, err := readWebPChunks(webp)
	if err != nil {
		return false
	}
	for _, c := range chunks {
		if c.id == "VP8X" && len(c.payload) > 0 {
			return c.payload[0]&0x02 != 0
		}
	}
	return false
}

// asAnimatedWebP embrulha um WebP de 1 quadro (simples VP8/VP8L, ou VP8X sem
// animação) num contêiner animado de 1 quadro: VP8X(anim) + ANIM + ANMF. O
// libwebp_anim grava imagem estática no formato simples, sem VP8X para o EXIF.
func asAnimatedWebP(webp []byte) ([]byte, error) {
	if webpIsAnimated(webp) {
		return webp, nil
	}
	chunks, err := readWebPChunks(webp)
	if err != nil {
		return nil, err
	}
	w, h := soundStickerSize, soundStickerSize
	var frame bytes.Buffer
	alpha := false
	for _, c := range chunks {
		switch c.id {
		case "VP8X":
			if len(c.payload) >= 10 {
				w = int(uint32(c.payload[4])|uint32(c.payload[5])<<8|uint32(c.payload[6])<<16) + 1
				h = int(uint32(c.payload[7])|uint32(c.payload[8])<<8|uint32(c.payload[9])<<16) + 1
			}
		case "VP8 ":
			if len(c.payload) >= 10 {
				w = int(binary.LittleEndian.Uint16(c.payload[6:8]) & 0x3fff)
				h = int(binary.LittleEndian.Uint16(c.payload[8:10]) & 0x3fff)
			}
		case "VP8L":
			if len(c.payload) >= 5 {
				v := binary.LittleEndian.Uint32(c.payload[1:5])
				w = int(v&0x3fff) + 1
				h = int((v>>14)&0x3fff) + 1
				alpha = alpha || (v>>28)&1 == 1
			}
		}
		if c.id == "ALPH" || c.id == "VP8 " || c.id == "VP8L" {
			if c.id == "ALPH" {
				alpha = true
			}
			var hd [8]byte
			copy(hd[:4], c.id)
			binary.LittleEndian.PutUint32(hd[4:], uint32(len(c.payload)))
			frame.Write(hd[:])
			frame.Write(c.payload)
			if len(c.payload)%2 == 1 {
				frame.WriteByte(0)
			}
		}
	}
	if frame.Len() == 0 {
		return nil, errors.New("WebP sem quadro de imagem")
	}
	put24 := func(b []byte, v int) { b[0], b[1], b[2] = byte(v), byte(v>>8), byte(v>>16) }
	vp8x := make([]byte, 10)
	vp8x[0] = 0x02 // animação
	if alpha {
		vp8x[0] |= 0x10
	}
	put24(vp8x[4:], w-1)
	put24(vp8x[7:], h-1)
	anim := []byte{0, 0, 0, 0, 0, 0} // fundo transparente, loop infinito
	anmf := make([]byte, 16)
	put24(anmf[6:], w-1)
	put24(anmf[9:], h-1)
	put24(anmf[12:], 1000) // 1 s
	anmf = append(anmf, frame.Bytes()...)
	return writeWebP([]webpChunk{{"VP8X", vp8x}, {"ANIM", anim}, {"ANMF", anmf}}), nil
}

type soundStickerPack struct {
	ID, Name, Publisher string
	Emojis              []string
}

// withSoundExif grava no EXIF do WebP o JSON da figurinha com "sound-sha256"
// (SHA-256 base64 do áudio). Exige WebP estendido (VP8X), que todo animado tem.
func withSoundExif(webp, audio []byte, pack soundStickerPack) ([]byte, error) {
	chunks, err := readWebPChunks(webp)
	if err != nil {
		return nil, err
	}
	found := false
	kept := chunks[:0]
	for _, c := range chunks {
		if c.id == "VP8X" {
			if len(c.payload) == 0 {
				return nil, errors.New("VP8X inválido")
			}
			c.payload[0] |= 0x08 // tem EXIF
			found = true
		}
		if c.id != "EXIF" {
			kept = append(kept, c)
		}
	}
	if !found {
		return nil, errors.New("o WebP não tem VP8X (precisa ser WebP animado)")
	}
	if pack.ID == "" {
		pack.ID = "astracalls"
	}
	if pack.Name == "" {
		pack.Name = "Figurinhas com som"
	}
	if pack.Publisher == "" {
		pack.Publisher = "AstraCalls"
	}
	if len(pack.Emojis) == 0 {
		pack.Emojis = []string{"🔊"}
	}
	sum := sha256.Sum256(audio)
	js, _ := json.Marshal(map[string]any{
		"sticker-pack-id":        pack.ID,
		"sticker-pack-name":      pack.Name,
		"sticker-pack-publisher": pack.Publisher,
		"emojis":                 pack.Emojis,
		"sound-sha256":           base64.StdEncoding.EncodeToString(sum[:]),
	})
	// TIFF little-endian com 1 entrada de IFD: tag 0x5741, tipo 7, count=len(json), offset 22.
	head := []byte{0x49, 0x49, 0x2a, 0x00, 0x08, 0x00, 0x00, 0x00, 0x01, 0x00, 0x41, 0x57, 0x07, 0x00, 0, 0, 0, 0, 0x16, 0x00, 0x00, 0x00}
	binary.LittleEndian.PutUint32(head[14:], uint32(len(js)))
	kept = append(kept, webpChunk{id: "EXIF", payload: append(head, js...)})
	return writeWebP(kept), nil
}

func runFFmpeg(input []byte, before, after []string) ([]byte, error) {
	tmp, err := os.CreateTemp("", "sndstk-*")
	if err != nil {
		return nil, err
	}
	defer os.Remove(tmp.Name())
	if _, err = tmp.Write(input); err != nil {
		tmp.Close()
		return nil, err
	}
	tmp.Close()
	out, err := os.CreateTemp("", "sndstk-out-*")
	if err != nil {
		return nil, err
	}
	out.Close()
	defer os.Remove(out.Name())
	args := append([]string{"-hide_banner", "-loglevel", "error", "-y"}, before...)
	args = append(args, "-i", tmp.Name())
	args = append(args, after...)
	args = append(args, out.Name())
	var stderr bytes.Buffer
	cmd := exec.Command("ffmpeg", args...)
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("ffmpeg: %v: %s", err, bytes.TrimSpace(stderr.Bytes()))
	}
	return os.ReadFile(out.Name())
}

func isMovingMedia(b []byte) bool {
	switch {
	case bytes.HasPrefix(b, []byte("GIF8")):
		return true
	case len(b) > 12 && string(b[4:8]) == "ftyp": // mp4/mov
		return true
	case bytes.HasPrefix(b, []byte{0x1a, 0x45, 0xdf, 0xa3}): // webm/mkv
		return true
	}
	return false
}

// toAnimatedSticker devolve um WebP animado 512x512. WebP animado passa como
// está; imagem estática vira animação de 1 s; GIF/vídeo é convertido (máx. 6 s).
func toAnimatedSticker(in []byte) ([]byte, error) {
	if webpIsAnimated(in) {
		return in, nil
	}
	vf := fmt.Sprintf("scale=%d:%d:force_original_aspect_ratio=decrease,format=rgba,pad=%d:%d:(ow-iw)/2:(oh-ih)/2:color=0x00000000,fps=15",
		soundStickerSize, soundStickerSize, soundStickerSize, soundStickerSize)
	var before []string
	if !isMovingMedia(in) {
		before = []string{"-loop", "1", "-t", "1"}
	}
	after := []string{"-t", "6", "-vf", vf, "-c:v", "libwebp_anim", "-loop", "0", "-q:v", "60", "-an", "-f", "webp"}
	out, err := runFFmpeg(in, before, after)
	if err != nil {
		return nil, fmt.Errorf("converter figurinha em WebP animado: %w", err)
	}
	if out, err = asAnimatedWebP(out); err != nil {
		return nil, err
	}
	if len(out) > 1<<20 {
		// passou de ~1 MB: refaz com qualidade menor
		after[8] = "30"
		if o2, e2 := runFFmpeg(in, before, after); e2 == nil {
			if o3, e3 := asAnimatedWebP(o2); e3 == nil {
				out = o3
			}
		}
	}
	return out, nil
}

// toOpusOgg devolve OGG/Opus (passa direto se já for) e a duração em segundos.
func toOpusOgg(in []byte) ([]byte, uint32, error) {
	if !(bytes.HasPrefix(in, []byte("OggS")) && bytes.Contains(in[:min(len(in), 200)], []byte("OpusHead"))) {
		out, err := runFFmpeg(in, nil, []string{"-t", "15", "-vn", "-ac", "1", "-ar", "48000", "-c:a", "libopus", "-b:a", "48k", "-f", "ogg"})
		if err != nil {
			return nil, 0, fmt.Errorf("converter áudio em OGG/Opus: %w", err)
		}
		in = out
	}
	sec := oggOpusDurationSeconds(in)
	if sec == 0 {
		sec = 1
	}
	return in, sec, nil
}

// buildSoundSticker monta o audioStickerMessage a partir dos dois uploads.
func buildSoundSticker(st, au whatsmeow.UploadResponse, seconds uint32, secret []byte) *waE2E.Message {
	return &waE2E.Message{
		MessageContextInfo: &waE2E.MessageContextInfo{MessageSecret: secret},
		AudioStickerMessage: &waE2E.FutureProofMessage{Message: &waE2E.Message{
			StickerMessage: &waE2E.StickerMessage{
				URL: proto.String(st.URL), DirectPath: proto.String(st.DirectPath), MediaKey: st.MediaKey,
				FileEncSHA256: st.FileEncSHA256, FileSHA256: st.FileSHA256, FileLength: proto.Uint64(st.FileLength),
				Mimetype: proto.String("image/webp"), Width: proto.Uint32(soundStickerSize), Height: proto.Uint32(soundStickerSize),
				IsAnimated: proto.Bool(true), StickerSentTS: proto.Int64(time.Now().UnixMilli()),
				Audio: &waE2E.StickerMessage_AudioMessage{AudioMessage: &waE2E.AudioMessage{
					URL: proto.String(au.URL), DirectPath: proto.String(au.DirectPath), MediaKey: au.MediaKey,
					FileEncSHA256: au.FileEncSHA256, FileSHA256: au.FileSHA256, FileLength: proto.Uint64(au.FileLength),
					Mimetype: proto.String("audio/ogg; codecs=opus"), Seconds: proto.Uint32(seconds),
				}},
			},
		}},
	}
}

// sendTypedDM envia uma mensagem 1:1 montando a stanza à mão (type="media" e
// <enc mediatype=...>), para envelopes que o whatsmeow não classifica. Espelha
// o sendDM do whatsmeow: destino em LID quando houver, cópia (DSM) para os
// nossos aparelhos, token de reporte e tctoken.
func (s *Session) sendTypedDM(ctx context.Context, to types.JID, msg *waE2E.Message, id, mediatype string) (time.Time, error) {
	if to.Server == types.GroupServer || to.Server == types.NewsletterServer || to.Server == types.BroadcastServer {
		return time.Time{}, errSoundStickerInput{"figurinha com som só é suportada em conversa privada"}
	}
	cli := s.client
	in := cli.DangerousInternals()
	to = s.canonicalBRTarget(ctx, to).ToNonAD()
	own := *cli.Store.ID
	var peerPN types.JID
	switch to.Server {
	case types.DefaultUserServer:
		lid, _ := cli.Store.LIDs.GetLIDForPN(ctx, to)
		if lid.IsEmpty() {
			if l, pn, ok := s.resolveCanonical(ctx, to); ok {
				cli.StoreLIDPNMapping(ctx, l, pn)
				lid, to = l, pn
			}
		}
		if !lid.IsEmpty() && !cli.Store.LID.IsEmpty() {
			peerPN, to, own = to, lid, cli.Store.LID
		}
	case types.HiddenUserServer:
		own = cli.Store.LID
		peerPN, _ = cli.Store.LIDs.GetPNForLID(ctx, to)
	}

	plaintext, err := proto.Marshal(msg)
	if err != nil {
		return time.Time{}, err
	}
	dsm, err := proto.Marshal(&waE2E.Message{
		DeviceSentMessage:  &waE2E.DeviceSentMessage{DestinationJID: proto.String(to.String()), Message: msg},
		MessageContextInfo: msg.MessageContextInfo,
	})
	if err != nil {
		return time.Time{}, err
	}
	devices, err := cli.GetUserDevicesContext(ctx, []types.JID{to, own.ToNonAD()})
	if err != nil {
		return time.Time{}, fmt.Errorf("aparelhos do destino: %w", err)
	}
	parts, includeIdentity, err := in.EncryptMessageForDevices(ctx, devices, id, plaintext, dsm, waBinary.Attrs{"mediatype": mediatype})
	if err != nil {
		return time.Time{}, fmt.Errorf("criptografar: %w", err)
	}
	if len(parts) == 0 {
		return time.Time{}, errors.New("nenhum aparelho de destino")
	}
	content := []waBinary.Node{{Tag: "participants", Content: parts}}
	if includeIdentity {
		content = append(content, in.MakeDeviceIdentityNode())
	}
	if in.ShouldIncludeReportingToken(msg) && msg.GetMessageContextInfo().GetMessageSecret() != nil {
		content = append(content, in.GetMessageReportingToken(plaintext, msg, own.ToNonAD(), to, id))
	}
	if tc, _ := in.EnsureTCToken(ctx, to); len(tc) > 0 {
		content = append(content, waBinary.Node{Tag: "tctoken", Content: tc})
	} else if cs := in.GenerateCsToken(ctx, to); len(cs) > 0 {
		content = append(content, waBinary.Node{Tag: "cstoken", Content: cs})
	}
	attrs := waBinary.Attrs{"id": id, "to": to, "type": "media"}
	if !peerPN.IsEmpty() {
		attrs["peer_recipient_pn"] = peerPN
	}
	ack, err := s.sendNodeAwait(ctx, waBinary.Node{Tag: "message", Attrs: attrs, Content: content}, id, 30*time.Second)
	if err != nil {
		return time.Time{}, err
	}
	if code, ok := ack.Attrs["error"]; ok {
		return time.Time{}, fmt.Errorf("WhatsApp recusou a mensagem (ack error=%v)", code)
	}
	ts := time.Now()
	if t, ok := ack.Attrs["t"]; ok {
		if n, err := strconv.ParseInt(fmt.Sprint(t), 10, 64); err == nil {
			ts = time.Unix(n, 0)
		}
	}
	return ts, nil
}

type errSoundStickerInput struct{ msg string }

func (e errSoundStickerInput) Error() string { return e.msg }

// POST /api/sessions/{sid}/messages/sound-sticker
// {to, sticker|stickerBase64|stickerUrl, audio|audioBase64|audioUrl, packName, publisher, emojis}
// Sem figurinha e sem áudio envia a figurinha de demonstração.
func (s *server) handleSendSoundSticker(w http.ResponseWriter, r *http.Request) {
	sess := s.pairedSession(w, r.PathValue("sid"))
	if sess == nil {
		return
	}
	var b struct {
		To            string   `json:"to"`
		Sticker       string   `json:"sticker"`
		StickerBase64 string   `json:"stickerBase64"`
		StickerURL    string   `json:"stickerUrl"`
		Audio         string   `json:"audio"`
		AudioBase64   string   `json:"audioBase64"`
		AudioURL      string   `json:"audioUrl"`
		PackName      string   `json:"packName"`
		Publisher     string   `json:"publisher"`
		Emojis        []string `json:"emojis"`
	}
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "JSON inválido"})
		return
	}
	jid, err := resolveRecipient(b.To)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	// "sticker"/"audio" aceitam URL http(s) ou base64/data URI.
	split := func(v, b64, u string) (string, string) {
		if v != "" {
			if len(v) > 8 && (v[:7] == "http://" || v[:8] == "https://") {
				return "", v
			}
			return v, ""
		}
		return b64, u
	}
	sb64, surl := split(b.Sticker, b.StickerBase64, b.StickerURL)
	ab64, aurl := split(b.Audio, b.AudioBase64, b.AudioURL)
	stickerIn, audioIn := soundStickerDefaultWebP, soundStickerDefaultOgg
	if sb64 != "" || surl != "" {
		if stickerIn, err = fetchMedia(sb64, surl); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "figurinha: " + err.Error()})
			return
		}
	}
	if ab64 != "" || aurl != "" {
		if audioIn, err = fetchMedia(ab64, aurl); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "áudio: " + err.Error()})
			return
		}
	}
	webp, err := toAnimatedSticker(stickerIn)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	ogg, seconds, err := toOpusOgg(audioIn)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	webp, err = withSoundExif(webp, ogg, soundStickerPack{Name: b.PackName, Publisher: b.Publisher, Emojis: b.Emojis})
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	ctx := r.Context()
	// upload novo a cada envio: referência de mídia reaproveitada é descartada em silêncio.
	st, err := sess.client.Upload(ctx, webp, whatsmeow.MediaImage)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "upload da figurinha: " + err.Error()})
		return
	}
	au, err := sess.client.Upload(ctx, ogg, whatsmeow.MediaAudio)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "upload do áudio: " + err.Error()})
		return
	}
	secret := make([]byte, 32)
	_, _ = rand.Read(secret)
	msg := buildSoundSticker(st, au, seconds, secret)
	id := messageIDFromRequest(r)
	if id == "" {
		id = string(sess.client.GenerateMessageID())
	}
	ts, err := sess.sendTypedDM(ctx, jid, msg, id, "sticker")
	if err != nil {
		var in errSoundStickerInput
		if errors.As(err, &in) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		code, body := sendErrorResponse(err)
		writeJSON(w, code, body)
		return
	}
	sess.recordOutgoing(jid, id, ts.UnixMilli(), msg)
	writeJSON(w, http.StatusOK, map[string]any{
		"id": id, "to": jid.String(), "timestamp": ts.UnixMilli(),
		"seconds": seconds, "stickerBytes": len(webp), "audioBytes": len(ogg),
	})
}

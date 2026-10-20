package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/socket"
	"go.mau.fi/whatsmeow/util/cbcutil"
	"go.mau.fi/whatsmeow/util/hkdfutil"
)

// uploadVoiceNote sobe uma nota de voz como o celular faz: no caminho /mms/ptt
// (objeto t62.7117 no CDN). O Upload da whatsmeow só conhece /mms/audio
// (t62.7114, "arquivo de áudio"); notas de voz subidas assim paravam de tocar no
// iPhone do destinatário em poucos minutos, enquanto as gravadas no celular
// seguiam tocando. A criptografia é a mesma (chaves "WhatsApp Audio Keys").
// Se o caminho ptt falhar, cai no Upload padrão para o envio não quebrar.
func (s *Session) uploadVoiceNote(ctx context.Context, data []byte) (whatsmeow.UploadResponse, error) {
	up, err := s.rawUploadAs(ctx, data, whatsmeow.MediaAudio, "ptt")
	if err == nil {
		return up, nil
	}
	s.log.Warn("upload de nota de voz como ptt falhou; usando /mms/audio", "err", err)
	return s.client.Upload(ctx, data, whatsmeow.MediaAudio)
}

// rawUploadAs replica o whatsmeow.Client.Upload escolhendo o tipo do caminho MMS.
func (s *Session) rawUploadAs(ctx context.Context, plaintext []byte, appInfo whatsmeow.MediaType, mmsType string) (resp whatsmeow.UploadResponse, err error) {
	resp.FileLength = uint64(len(plaintext))
	resp.MediaKey = make([]byte, 32)
	if _, err = rand.Read(resp.MediaKey); err != nil {
		return
	}
	sum := sha256.Sum256(plaintext)
	resp.FileSHA256 = sum[:]

	keys := hkdfutil.SHA256(resp.MediaKey, nil, []byte(appInfo), 112)
	iv, cipherKey, macKey := keys[:16], keys[16:48], keys[48:80]
	ciphertext, err := cbcutil.Encrypt(cipherKey, iv, plaintext)
	if err != nil {
		return resp, fmt.Errorf("criptografia: %w", err)
	}
	h := hmac.New(sha256.New, macKey)
	h.Write(iv)
	h.Write(ciphertext)
	body := append(ciphertext, h.Sum(nil)[:10]...)
	encSum := sha256.Sum256(body)
	resp.FileEncSHA256 = encSum[:]

	mc, err := s.client.DangerousInternals().RefreshMediaConn(ctx, false)
	if err != nil {
		return resp, fmt.Errorf("media conn: %w", err)
	}
	if len(mc.Hosts) == 0 {
		return resp, fmt.Errorf("media conn sem hosts")
	}
	token := base64.URLEncoding.EncodeToString(resp.FileEncSHA256)
	u := url.URL{
		Scheme:   "https",
		Host:     mc.Hosts[0].Hostname,
		Path:     fmt.Sprintf("/mms/%s/%s", mmsType, token),
		RawQuery: url.Values{"auth": {mc.Auth}, "token": {token}}.Encode(),
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(body))
	if err != nil {
		return resp, err
	}
	req.ContentLength = int64(len(body))
	req.Header.Set("Origin", socket.Origin)
	req.Header.Set("Referer", socket.Origin+"/")
	hc := &http.Client{Timeout: 2 * time.Minute}
	httpResp, err := hc.Do(req)
	if err != nil {
		return resp, err
	}
	defer httpResp.Body.Close()
	if httpResp.StatusCode != http.StatusOK {
		return resp, fmt.Errorf("upload %s: status %d", mmsType, httpResp.StatusCode)
	}
	if err = json.NewDecoder(httpResp.Body).Decode(&resp); err != nil {
		return resp, fmt.Errorf("resposta do upload: %w", err)
	}
	if resp.DirectPath == "" {
		return resp, fmt.Errorf("upload %s sem direct_path", mmsType)
	}
	return resp, nil
}

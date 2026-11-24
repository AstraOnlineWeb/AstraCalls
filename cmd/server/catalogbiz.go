package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.mau.fi/whatsmeow"
	waBinary "go.mau.fi/whatsmeow/binary"
	"go.mau.fi/whatsmeow/socket"
	"go.mau.fi/whatsmeow/types"
	waLog "go.mau.fi/whatsmeow/util/log"
)

// Catálogo do WhatsApp Business pelo fluxo do DONO (o mesmo do WhatsApp Web):
//
//  1. IQ fb:thrift_iq smax_id=118 → o servidor manda uma <notification type="business">
//     com <wa_ad_account_nonce>;
//  2. IQ fb:thrift_iq smax_id=104 com <parameters><code>NONCE</code></parameters>
//     → <access_token>;
//  3. POST https://graph.facebook.com/graphql {access_token, doc_id, variables, locale}.
//
// doc_ids das mutations do WA Web (WAWebBizCatalogManagement*), extraídos pela lib
// Cobalt (Auties00/Cobalt, tools/web/graphql-extractor). O IQ antigo w:biz:catalog
// (Baileys) não tem mais resposta do servidor desde ~jul/2026.
const (
	bizGraphURL        = "https://graph.facebook.com/graphql"
	bizDocAddProduct   = "24249359867999500"
	bizDocEditProduct  = "9889773371084956"
	bizDocDeleteProd   = "9376108569185474"
	bizDocFetchCatalog = "9957894520961099"
	bizNonceWait       = 20 * time.Second
	bizTokenTTL        = 30 * time.Minute
)

// ---- captura do nonce ----
// O whatsmeow não trata notification type="business": só loga o nó recebido no
// logger "Recv" (Debugf("%s", *Node)). Este logger embrulha o do whatsmeow e
// entrega o nonce a quem estiver esperando. Só um pedido de token por vez (bizMu).

var (
	bizMu      sync.Mutex
	bizNonceCh = make(chan string, 1)
)

type nonceTapLogger struct {
	base waLog.Logger
	recv bool
}

func newNonceTapLogger(base waLog.Logger) waLog.Logger { return nonceTapLogger{base: base} }

func (l nonceTapLogger) Debugf(msg string, args ...any) {
	if l.recv && len(args) == 1 {
		if n, ok := args[0].(*waBinary.Node); ok && n.Tag == "notification" {
			if c, ok := n.GetOptionalChildByTag("wa_ad_account_nonce"); ok {
				if v := nodeContentString(c); v != "" {
					select {
					case bizNonceCh <- v:
					default:
					}
				}
			}
		}
	}
	l.base.Debugf(msg, args...)
}
func (l nonceTapLogger) Infof(msg string, args ...any)  { l.base.Infof(msg, args...) }
func (l nonceTapLogger) Warnf(msg string, args ...any)  { l.base.Warnf(msg, args...) }
func (l nonceTapLogger) Errorf(msg string, args ...any) { l.base.Errorf(msg, args...) }
func (l nonceTapLogger) Sub(module string) waLog.Logger {
	return nonceTapLogger{base: l.base.Sub(module), recv: module == "Recv"}
}

func nodeContentString(n waBinary.Node) string {
	switch v := n.Content.(type) {
	case []byte:
		return strings.TrimSpace(string(v))
	case string:
		return strings.TrimSpace(v)
	}
	return ""
}

// ---- token do dono ----

type bizToken struct {
	token string
	exp   time.Time
}

var bizTokens sync.Map // sessão id → bizToken

func (s *Session) bizAccessToken(ctx context.Context, force bool) (string, error) {
	if !force {
		if v, ok := bizTokens.Load(s.id); ok {
			if t := v.(bizToken); time.Now().Before(t.exp) {
				return t.token, nil
			}
		}
	}
	bizMu.Lock()
	defer bizMu.Unlock()
	select { // descarta nonce velho
	case <-bizNonceCh:
	default:
	}
	in := s.client.DangerousInternals()
	resp, err := in.SendIQ(ctx, whatsmeow.DangerousInfoQuery{
		Namespace: "fb:thrift_iq", Type: "get", To: types.ServerJID, SMaxID: "118",
	})
	if err != nil {
		return "", fmt.Errorf("pedido de nonce: %w", err)
	}
	if r, ok := resp.GetOptionalChildByTag("result"); ok {
		if st := r.AttrGetter().OptionalString("status"); st != "" && st != "Success" {
			return "", fmt.Errorf("WhatsApp respondeu %s ao pedir acesso ao catálogo (conta Business?)", st)
		}
	}
	var nonce string
	select {
	case nonce = <-bizNonceCh:
	case <-time.After(bizNonceWait):
		return "", errors.New("o WhatsApp não mandou o código de acesso do catálogo (a conta é WhatsApp Business?)")
	case <-ctx.Done():
		return "", ctx.Err()
	}
	resp, err = in.SendIQ(ctx, whatsmeow.DangerousInfoQuery{
		Namespace: "fb:thrift_iq", Type: "get", To: types.ServerJID, SMaxID: "104",
		Content: []waBinary.Node{{Tag: "parameters", Content: []waBinary.Node{{Tag: "code", Content: []byte(nonce)}}}},
	})
	if err != nil {
		return "", fmt.Errorf("troca do nonce: %w", err)
	}
	at, ok := resp.GetOptionalChildByTag("access_token")
	if !ok {
		// às vezes vem dentro de <result>
		if r, ok2 := resp.GetOptionalChildByTag("result"); ok2 {
			at, ok = r.GetOptionalChildByTag("access_token")
		}
	}
	tok := ""
	if ok {
		tok = nodeContentString(at)
	}
	if tok == "" {
		return "", errors.New("o WhatsApp não devolveu o token do catálogo")
	}
	bizTokens.Store(s.id, bizToken{token: tok, exp: time.Now().Add(bizTokenTTL)})
	s.log.Info("token do catálogo obtido")
	return tok, nil
}

// bizGraphQL chama o GraphQL do dono; renova o token uma vez se ele expirou.
func (s *Session) bizGraphQL(ctx context.Context, docID string, variables any) (map[string]json.RawMessage, error) {
	for attempt := 0; attempt < 2; attempt++ {
		tok, err := s.bizAccessToken(ctx, attempt > 0)
		if err != nil {
			return nil, err
		}
		body, _ := json.Marshal(map[string]any{
			"access_token": tok, "doc_id": docID, "variables": variables, "locale": "pt_BR",
		})
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, bizGraphURL, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json")
		req.Header.Set("Origin", socket.Origin)
		resp, err := (&http.Client{Timeout: 40 * time.Second}).Do(req)
		if err != nil {
			return nil, err
		}
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		resp.Body.Close()
		var out struct {
			Data   map[string]json.RawMessage `json:"data"`
			Errors []struct {
				Message     string `json:"message"`
				Summary     string `json:"summary"`
				Description string `json:"description"`
				Code        int    `json:"code"`
			} `json:"errors"`
			Error *struct {
				Message string `json:"message"`
				Code    int    `json:"code"`
			} `json:"error"`
		}
		if err := json.Unmarshal(raw, &out); err != nil {
			return nil, fmt.Errorf("resposta inválida do catálogo (HTTP %d): %.200s", resp.StatusCode, raw)
		}
		if out.Error != nil {
			if attempt == 0 && (out.Error.Code == 190 || resp.StatusCode == 401) {
				continue // token expirado → renova
			}
			return nil, fmt.Errorf("catálogo: %s (code %d)", out.Error.Message, out.Error.Code)
		}
		if len(out.Errors) > 0 {
			e := out.Errors[0]
			msg := e.Description
			if msg == "" {
				msg = e.Summary
			}
			if msg == "" {
				msg = e.Message
			}
			return out.Data, fmt.Errorf("catálogo: %s (code %d)", msg, e.Code)
		}
		return out.Data, nil
	}
	return nil, errors.New("catálogo: token recusado")
}

// ---- foto do produto ----

// uploadProductImage sobe a foto SEM criptografia em /product/image (como o
// WhatsApp Web faz para catálogo) e devolve a URL mmg.whatsapp.net.
func (s *Session) uploadProductImage(ctx context.Context, data []byte) (string, error) {
	sum := sha256.Sum256(data)
	mc, err := s.client.DangerousInternals().RefreshMediaConn(ctx, false)
	if err != nil {
		return "", fmt.Errorf("media conn: %w", err)
	}
	if len(mc.Hosts) == 0 {
		return "", errors.New("media conn sem hosts")
	}
	token := base64.URLEncoding.EncodeToString(sum[:])
	u := url.URL{
		Scheme:   "https",
		Host:     mc.Hosts[0].Hostname,
		Path:     "/product/image/" + token,
		RawQuery: url.Values{"auth": {mc.Auth}, "token": {token}}.Encode(),
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(data))
	if err != nil {
		return "", err
	}
	req.ContentLength = int64(len(data))
	req.Header.Set("Origin", socket.Origin)
	req.Header.Set("Referer", socket.Origin+"/")
	resp, err := (&http.Client{Timeout: 2 * time.Minute}).Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("upload da foto do produto: status %d", resp.StatusCode)
	}
	var out struct {
		URL        string `json:"url"`
		DirectPath string `json:"direct_path"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", fmt.Errorf("resposta do upload: %w", err)
	}
	if out.DirectPath != "" {
		return "https://mmg.whatsapp.net" + out.DirectPath, nil
	}
	if out.URL != "" {
		return out.URL, nil
	}
	return "", errors.New("upload da foto sem direct_path")
}

// ---- produto ----

type bizProductInput struct {
	Name        *string  `json:"name"`
	Description *string  `json:"description"`
	Price       *float64 `json:"price"`     // em reais (199.90)
	SalePrice   *float64 `json:"salePrice"` // promoção, opcional
	Currency    *string  `json:"currency"`
	RetailerID  *string  `json:"retailerId"`
	URL         *string  `json:"url"`
	IsHidden    *bool    `json:"isHidden"`
	Images      []string `json:"images"`    // base64
	ImageURLs   []string `json:"imageUrls"` // URLs
	Origin      *string  `json:"originCountryCode"`
}

// O WhatsApp guarda preço em milésimos (199.90 → "199900"), como o priceAmount1000.
func bizPrice(v float64) string { return strconv.FormatInt(int64(math.Round(v*1000)), 10) }

func (in bizProductInput) productInfo(images []string, create bool) map[string]any {
	pi := map[string]any{}
	set := func(k string, v *string) {
		if v != nil {
			pi[k] = *v
		}
	}
	set("name", in.Name)
	set("description", in.Description)
	set("url", in.URL)
	set("retailer_id", in.RetailerID)
	if in.Currency != nil {
		pi["currency"] = strings.ToUpper(*in.Currency)
	}
	if in.Price != nil {
		pi["price"] = bizPrice(*in.Price)
	}
	if in.SalePrice != nil && *in.SalePrice > 0 {
		pi["sale_price"] = bizPrice(*in.SalePrice)
	}
	if in.IsHidden != nil {
		pi["is_hidden"] = *in.IsHidden
	} else if create {
		pi["is_hidden"] = false
	}
	if len(images) > 0 {
		imgs := make([]map[string]string, 0, len(images))
		for _, u := range images {
			imgs = append(imgs, map[string]string{"url": u})
		}
		pi["media"] = map[string]any{"image": imgs}
	}
	if create {
		if in.Origin != nil && strings.TrimSpace(*in.Origin) != "" {
			pi["compliance_info"] = map[string]string{"country_code_origin": strings.ToUpper(strings.TrimSpace(*in.Origin))}
		} else {
			pi["compliance_category"] = "COUNTRY_ORIGIN_EXEMPT"
		}
	}
	return pi
}

func (s *Session) bizProductImages(ctx context.Context, in bizProductInput) ([]string, error) {
	var out []string
	for _, b64 := range in.Images {
		data, err := fetchMedia(b64, "")
		if err != nil {
			return nil, fmt.Errorf("imagem base64 inválida: %w", err)
		}
		u, err := s.uploadProductImage(ctx, data)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	for _, src := range in.ImageURLs {
		if strings.Contains(src, ".whatsapp.net") {
			out = append(out, src)
			continue
		}
		data, err := fetchMedia("", src)
		if err != nil {
			return nil, fmt.Errorf("imagem %s: %w", src, err)
		}
		u, err := s.uploadProductImage(ctx, data)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, nil
}

func (s *Session) bizOwnJID() (string, error) {
	if s.client.Store.ID == nil {
		return "", errors.New("sessão não pareada")
	}
	return s.client.Store.ID.ToNonAD().String(), nil
}

// bizNoCatalog: erros do GraphQL quando a conta Business ainda não tem catálogo.
func bizNoCatalog(err error) bool {
	if err == nil {
		return false
	}
	m := err.Error()
	return strings.Contains(m, "2498049") || strings.Contains(m, "2498052")
}

// bizJIDCandidates: telefone e, se houver, o LID da própria conta.
func (s *Session) bizJIDCandidates(pn string) []string {
	out := []string{pn}
	if lid := s.client.Store.LID; !lid.IsEmpty() {
		out = append(out, lid.ToNonAD().String())
	}
	return out
}

// bizResult escreve a resposta: 200 com o nó de dados, ou 502 com o erro.
func bizResult(w http.ResponseWriter, data map[string]json.RawMessage, key string, err error) {
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	if v, ok := data[key]; ok {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(v)
		return
	}
	writeJSON(w, http.StatusOK, data)
}

// GET /api/sessions/{sid}/catalog?limit=&cursor=  (catálogo da própria conta Business)
func (s *server) handleGetCatalog(w http.ResponseWriter, r *http.Request) {
	sess := s.pairedSession(w, r.PathValue("sid"))
	if sess == nil {
		return
	}
	jid, err := sess.bizOwnJID()
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": err.Error()})
		return
	}
	limit := "10"
	if v, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && v > 0 && v <= 100 {
		limit = strconv.Itoa(v)
	}
	pc := map[string]any{"jid": jid, "limit": limit, "width": "100", "height": "100", "allow_shop_source": false}
	if c := r.URL.Query().Get("cursor"); c != "" {
		pc["after"] = c
	}
	data, err := sess.bizGraphQL(r.Context(), bizDocFetchCatalog, map[string]any{
		"request": map[string]any{"product_catalog": pc, "platform": "WEB"},
	})
	bizResult(w, data, "xfb_whatsapp_catalog", err)
}

// POST /api/sessions/{sid}/catalog/products → cadastra no catálogo da conta Business.
func (s *server) handleCreateProduct(w http.ResponseWriter, r *http.Request) {
	sess := s.pairedSession(w, r.PathValue("sid"))
	if sess == nil {
		return
	}
	var in bizProductInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "payload inválido"})
		return
	}
	if in.Name == nil || strings.TrimSpace(*in.Name) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name obrigatório"})
		return
	}
	if in.Price == nil || *in.Price < 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "price obrigatório (em reais, ex.: 199.90)"})
		return
	}
	if in.Currency == nil || strings.TrimSpace(*in.Currency) == "" {
		c := "BRL"
		in.Currency = &c
	}
	if len(in.Images)+len(in.ImageURLs) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "envie ao menos uma foto (images base64 ou imageUrls)"})
		return
	}
	jid, err := sess.bizOwnJID()
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": err.Error()})
		return
	}
	imgs, err := sess.bizProductImages(r.Context(), in)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	// o WhatsApp Web novo identifica a conta pelo LID; tenta telefone e depois LID
	info := in.productInfo(imgs, true)
	var data map[string]json.RawMessage
	for _, bj := range sess.bizJIDCandidates(jid) {
		data, err = sess.bizGraphQL(r.Context(), bizDocAddProduct, map[string]any{
			"input": map[string]any{"product": map[string]any{
				"biz_jid": bj, "width": 100, "height": 100, "product_info": info,
			}},
		})
		if !bizNoCatalog(err) {
			break
		}
		sess.log.Info("cadastro de produto recusado com este biz_jid; tentando o próximo", "biz_jid", bj, "err", err)
	}
	if err == nil {
		sess.log.Info("produto cadastrado no catálogo")
	}
	bizResult(w, data, "xfb_whatsapp_catalog_add_product", err)
}

// PUT /api/sessions/{sid}/catalog/products/{id} → edita (campos ausentes ficam como estão).
func (s *server) handleUpdateProduct(w http.ResponseWriter, r *http.Request) {
	sess := s.pairedSession(w, r.PathValue("sid"))
	if sess == nil {
		return
	}
	var in bizProductInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "payload inválido"})
		return
	}
	jid, err := sess.bizOwnJID()
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": err.Error()})
		return
	}
	imgs, err := sess.bizProductImages(r.Context(), in)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	data, err := sess.bizGraphQL(r.Context(), bizDocEditProduct, map[string]any{
		"input": map[string]any{"product": map[string]any{
			"biz_jid": jid, "product_id": r.PathValue("id"), "width": "100", "height": "100",
			"product_info": in.productInfo(imgs, false),
		}},
	})
	bizResult(w, data, "xfb_whatsapp_catalog_edit_product", err)
}

// DELETE /api/sessions/{sid}/catalog/products/{id}  (outros ids: ?ids=a,b)
func (s *server) handleDeleteProduct(w http.ResponseWriter, r *http.Request) {
	sess := s.pairedSession(w, r.PathValue("sid"))
	if sess == nil {
		return
	}
	jid, err := sess.bizOwnJID()
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": err.Error()})
		return
	}
	ids := []string{strings.TrimSpace(r.PathValue("id"))}
	for _, x := range strings.Split(r.URL.Query().Get("ids"), ",") {
		if x = strings.TrimSpace(x); x != "" {
			ids = append(ids, x)
		}
	}
	data, err := sess.bizGraphQL(r.Context(), bizDocDeleteProd, map[string]any{
		"input": map[string]any{"biz_jid": jid, "product_ids": ids},
	})
	bizResult(w, data, "xfb_whatsapp_catalog_delete_product", err)
}

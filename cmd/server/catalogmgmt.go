package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"go.mau.fi/whatsmeow"
	waBinary "go.mau.fi/whatsmeow/binary"
	"go.mau.fi/whatsmeow/socket"
	"go.mau.fi/whatsmeow/types"
)

// Catálogo do WhatsApp Business (porte do Baileys src/Socket/business.ts).
// Cadastrar/editar/apagar só funciona se a conta conectada for WhatsApp Business;
// listar funciona para o catálogo de qualquer conta Business (jid).
// Preço: a API recebe em reais (199.90) e o WhatsApp guarda em milésimos (199900),
// o mesmo formato do priceAmount1000 da mensagem de produto.

// catalogProduct é o produto como a API devolve.
type catalogProduct struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Price       float64  `json:"price"`
	Currency    string   `json:"currency,omitempty"`
	RetailerID  string   `json:"retailerId,omitempty"`
	URL         string   `json:"url,omitempty"`
	ImageURLs   []string `json:"imageUrls,omitempty"`
	IsHidden    bool     `json:"isHidden"`
	Status      string   `json:"reviewStatus,omitempty"` // análise do WhatsApp (APPROVED, PENDING...)
}

// catalogProductInput é o corpo de criar/editar.
type catalogProductInput struct {
	Name        *string  `json:"name"`
	Description *string  `json:"description"`
	Price       *float64 `json:"price"`
	Currency    *string  `json:"currency"`
	RetailerID  *string  `json:"retailerId"`
	URL         *string  `json:"url"`
	IsHidden    *bool    `json:"isHidden"`
	Images      []string `json:"images"`    // base64 (ou data:)
	ImageURLs   []string `json:"imageUrls"` // URLs (baixadas e reenviadas ao WhatsApp)
	Origin      *string  `json:"originCountryCode"`
}

func priceToWA(v float64) string { return strconv.FormatInt(int64(math.Round(v*1000)), 10) }

func priceFromWA(s string) float64 {
	n, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		return 0
	}
	return n / 1000
}

func textNode(tag, val string) waBinary.Node {
	return waBinary.Node{Tag: tag, Content: []byte(val)}
}

func nodeText(n *waBinary.Node, tag string) string {
	if n == nil {
		return ""
	}
	c, ok := n.GetOptionalChildByTag(tag)
	if !ok {
		return ""
	}
	switch v := c.Content.(type) {
	case []byte:
		return string(v)
	case string:
		return v
	}
	return ""
}

// toProductNode monta o <product> (Baileys toProductNode). images já são URLs do WhatsApp.
func toProductNode(id string, in catalogProductInput, images []string, create bool) waBinary.Node {
	attrs := waBinary.Attrs{}
	var content []waBinary.Node
	if id != "" {
		content = append(content, textNode("id", id))
	}
	if in.Name != nil {
		content = append(content, textNode("name", *in.Name))
	}
	if in.Description != nil {
		content = append(content, textNode("description", *in.Description))
	}
	if in.RetailerID != nil {
		content = append(content, textNode("retailer_id", *in.RetailerID))
	}
	if len(images) > 0 {
		imgs := make([]waBinary.Node, 0, len(images))
		for _, u := range images {
			imgs = append(imgs, waBinary.Node{Tag: "image", Content: []waBinary.Node{textNode("url", u)}})
		}
		content = append(content, waBinary.Node{Tag: "media", Content: imgs})
	}
	if in.Price != nil {
		content = append(content, textNode("price", priceToWA(*in.Price)))
	}
	if in.Currency != nil {
		content = append(content, textNode("currency", strings.ToUpper(*in.Currency)))
	}
	if in.URL != nil {
		content = append(content, textNode("url", *in.URL))
	}
	if create {
		if in.Origin != nil && strings.TrimSpace(*in.Origin) != "" {
			content = append(content, waBinary.Node{Tag: "compliance_info", Content: []waBinary.Node{
				textNode("country_code_origin", strings.ToUpper(strings.TrimSpace(*in.Origin)))}})
		} else {
			attrs["compliance_category"] = "COUNTRY_ORIGIN_EXEMPT"
		}
	}
	if in.IsHidden != nil {
		attrs["is_hidden"] = strconv.FormatBool(*in.IsHidden)
	} else if create {
		attrs["is_hidden"] = "false"
	}
	return waBinary.Node{Tag: "product", Attrs: attrs, Content: content}
}

func parseProductNode(n *waBinary.Node) catalogProduct {
	p := catalogProduct{
		ID:          nodeText(n, "id"),
		Name:        nodeText(n, "name"),
		Description: nodeText(n, "description"),
		Price:       priceFromWA(nodeText(n, "price")),
		Currency:    nodeText(n, "currency"),
		RetailerID:  nodeText(n, "retailer_id"),
		URL:         nodeText(n, "url"),
		IsHidden:    n.AttrGetter().OptionalString("is_hidden") == "true",
	}
	if st, ok := n.GetOptionalChildByTag("status_info"); ok {
		p.Status = nodeText(&st, "status")
	}
	if media, ok := n.GetOptionalChildByTag("media"); ok {
		for _, img := range media.GetChildrenByTag("image") {
			if u := nodeText(&img, "request_image_url"); u != "" {
				p.ImageURLs = append(p.ImageURLs, u)
			} else if u := nodeText(&img, "original_image_url"); u != "" {
				p.ImageURLs = append(p.ImageURLs, u)
			} else if u := nodeText(&img, "url"); u != "" {
				p.ImageURLs = append(p.ImageURLs, u)
			}
		}
	}
	return p
}

func (s *Session) catalogIQ(ctx context.Context, typ string, content waBinary.Node) (*waBinary.Node, error) {
	q := whatsmeow.DangerousInfoQuery{Namespace: "w:biz:catalog", Type: whatsmeow.DangerousInfoQueryType(typ), To: types.ServerJID, Content: []waBinary.Node{content}}
	resp, err := s.client.DangerousInternals().SendIQ(ctx, q)
	if err != nil {
		return nil, err
	}
	if err := firstNodeError(resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// uploadProductImage sobe a foto do produto SEM criptografia em /product/image
// (Baileys: mediaType product-catalog-image) e devolve a URL mmg.whatsapp.net.
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

// productImages baixa/decodifica as fotos e sobe ao WhatsApp. URL que já é do
// WhatsApp passa direto.
func (s *Session) productImages(ctx context.Context, in catalogProductInput) ([]string, error) {
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

func dims() []waBinary.Node {
	return []waBinary.Node{textNode("width", "100"), textNode("height", "100")}
}

func (s *Session) catalogJID(raw string) (types.JID, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		if s.client.Store.ID == nil {
			return types.EmptyJID, errors.New("sessão não pareada")
		}
		return s.client.Store.ID.ToNonAD(), nil
	}
	return resolveRecipient(raw)
}

// GET /api/sessions/{sid}/catalog?jid=&limit=&cursor=
func (s *server) handleGetCatalog(w http.ResponseWriter, r *http.Request) {
	sess := s.pairedSession(w, r.PathValue("sid"))
	if sess == nil {
		return
	}
	jid, err := sess.catalogJID(r.URL.Query().Get("jid"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	limit := 10
	if v, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && v > 0 && v <= 100 {
		limit = v
	}
	params := append([]waBinary.Node{textNode("limit", strconv.Itoa(limit))}, dims()...)
	if c := r.URL.Query().Get("cursor"); c != "" {
		params = append(params, textNode("after", c))
	}
	resp, err := sess.catalogIQ(r.Context(), "get", waBinary.Node{
		Tag:     "product_catalog",
		Attrs:   waBinary.Attrs{"jid": jid, "allow_shop_source": "true"},
		Content: params,
	})
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	cat, _ := resp.GetOptionalChildByTag("product_catalog")
	products := []catalogProduct{}
	for _, p := range cat.GetChildrenByTag("product") {
		products = append(products, parseProductNode(&p))
	}
	out := map[string]any{"jid": jid.String(), "products": products}
	if pg, ok := cat.GetOptionalChildByTag("paging"); ok {
		if after := nodeText(&pg, "after"); after != "" {
			out["nextCursor"] = after
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// GET /api/sessions/{sid}/catalog/collections?jid=&limit=
func (s *server) handleGetCollections(w http.ResponseWriter, r *http.Request) {
	sess := s.pairedSession(w, r.PathValue("sid"))
	if sess == nil {
		return
	}
	jid, err := sess.catalogJID(r.URL.Query().Get("jid"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	limit := "51"
	if v, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && v > 0 && v <= 100 {
		limit = strconv.Itoa(v)
	}
	q := whatsmeow.DangerousInfoQuery{
		Namespace: "w:biz:catalog", Type: "get", To: types.ServerJID, SMaxID: "35",
		Content: []waBinary.Node{{
			Tag:   "collections",
			Attrs: waBinary.Attrs{"biz_jid": jid},
			Content: append([]waBinary.Node{textNode("collection_limit", limit), textNode("item_limit", limit)},
				dims()...),
		}},
	}
	resp, err := sess.client.DangerousInternals().SendIQ(r.Context(), q)
	if err == nil {
		err = firstNodeError(resp)
	}
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	cols, _ := resp.GetOptionalChildByTag("collections")
	out := []map[string]any{}
	for _, c := range cols.GetChildrenByTag("collection") {
		products := []catalogProduct{}
		for _, p := range c.GetChildrenByTag("product") {
			products = append(products, parseProductNode(&p))
		}
		item := map[string]any{"id": nodeText(&c, "id"), "name": nodeText(&c, "name"), "products": products}
		if st, ok := c.GetOptionalChildByTag("status_info"); ok {
			item["status"] = nodeText(&st, "status")
		}
		out = append(out, item)
	}
	writeJSON(w, http.StatusOK, map[string]any{"jid": jid.String(), "collections": out})
}

// POST /api/sessions/{sid}/catalog/products → cadastra; devolve o produto com id.
func (s *server) handleCreateProduct(w http.ResponseWriter, r *http.Request) {
	sess := s.pairedSession(w, r.PathValue("sid"))
	if sess == nil {
		return
	}
	var in catalogProductInput
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
	if in.Description == nil {
		d := ""
		in.Description = &d
	}
	if len(in.Images)+len(in.ImageURLs) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "envie ao menos uma foto (images base64 ou imageUrls)"})
		return
	}
	imgs, err := sess.productImages(r.Context(), in)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	resp, err := sess.catalogIQ(r.Context(), "set", waBinary.Node{
		Tag: "product_catalog_add", Attrs: waBinary.Attrs{"v": "1"},
		Content: append([]waBinary.Node{toProductNode("", in, imgs, true)}, dims()...),
	})
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": catalogErr(err)})
		return
	}
	add, _ := resp.GetOptionalChildByTag("product_catalog_add")
	p, ok := add.GetOptionalChildByTag("product")
	if !ok {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "WhatsApp não devolveu o produto"})
		return
	}
	sess.log.Info("produto cadastrado no catálogo", "id", nodeText(&p, "id"))
	writeJSON(w, http.StatusOK, parseProductNode(&p))
}

// PUT /api/sessions/{sid}/catalog/products/{id} → edita (campos ausentes ficam como estão).
func (s *server) handleUpdateProduct(w http.ResponseWriter, r *http.Request) {
	sess := s.pairedSession(w, r.PathValue("sid"))
	if sess == nil {
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	var in catalogProductInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "payload inválido"})
		return
	}
	imgs, err := sess.productImages(r.Context(), in)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	resp, err := sess.catalogIQ(r.Context(), "set", waBinary.Node{
		Tag: "product_catalog_edit", Attrs: waBinary.Attrs{"v": "1"},
		Content: append([]waBinary.Node{toProductNode(id, in, imgs, false)}, dims()...),
	})
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": catalogErr(err)})
		return
	}
	ed, _ := resp.GetOptionalChildByTag("product_catalog_edit")
	p, ok := ed.GetOptionalChildByTag("product")
	if !ok {
		writeJSON(w, http.StatusOK, map[string]any{"id": id, "ok": true})
		return
	}
	writeJSON(w, http.StatusOK, parseProductNode(&p))
}

// DELETE /api/sessions/{sid}/catalog/products/{id}  (ids extras: ?ids=a,b)
func (s *server) handleDeleteProduct(w http.ResponseWriter, r *http.Request) {
	sess := s.pairedSession(w, r.PathValue("sid"))
	if sess == nil {
		return
	}
	ids := []string{strings.TrimSpace(r.PathValue("id"))}
	for _, x := range strings.Split(r.URL.Query().Get("ids"), ",") {
		if x = strings.TrimSpace(x); x != "" {
			ids = append(ids, x)
		}
	}
	prods := make([]waBinary.Node, 0, len(ids))
	for _, id := range ids {
		if id != "" {
			prods = append(prods, waBinary.Node{Tag: "product", Content: []waBinary.Node{textNode("id", id)}})
		}
	}
	resp, err := sess.catalogIQ(r.Context(), "set", waBinary.Node{
		Tag: "product_catalog_delete", Attrs: waBinary.Attrs{"v": "1"}, Content: prods,
	})
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": catalogErr(err)})
		return
	}
	del, _ := resp.GetOptionalChildByTag("product_catalog_delete")
	n, _ := strconv.Atoi(del.AttrGetter().OptionalString("deleted_count"))
	writeJSON(w, http.StatusOK, map[string]any{"deleted": n})
}

// catalogErr deixa claro o caso mais comum: conta que não é Business.
func catalogErr(err error) string {
	msg := err.Error()
	if strings.Contains(msg, "403") || strings.Contains(msg, "401") || strings.Contains(msg, "not-authorized") || strings.Contains(msg, "forbidden") {
		return msg + " — cadastrar produto exige conta WhatsApp Business"
	}
	return msg
}

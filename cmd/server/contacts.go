package main

import (
	"net/http"
	"strings"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

// GET /api/sessions/{sid}/contacts/check?phone=...
// Consulta autoritativa ao WhatsApp: o número existe? Qual o JID canônico?
// Resolve o problema do 9º dígito (BR) — devolve o JID real independente de o
// número ter sido digitado com ou sem o 9.
func (s *server) handleCheckNumber(w http.ResponseWriter, r *http.Request) {
	sess := s.pairedSession(w, r.PathValue("sid"))
	if sess == nil {
		return
	}
	phone := strings.TrimSpace(r.URL.Query().Get("phone"))
	if phone == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "phone required"})
		return
	}
	if !strings.HasPrefix(phone, "+") {
		phone = "+" + normalizePhone(phone)
	}
	resp, err := sess.client.IsOnWhatsApp(r.Context(), []string{phone})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if len(resp) == 0 {
		writeJSON(w, http.StatusOK, map[string]any{"query": phone, "exists": false, "numberExists": false})
		return
	}
	it := resp[0]
	out := map[string]any{"query": it.Query, "exists": it.IsIn, "numberExists": it.IsIn}
	if it.IsIn {
		// contas migradas para LID: o WhatsApp devolve o @lid em JID e o telefone
		// em PhoneNumber. jid/number seguem sendo o telefone (contrato da doc);
		// o LID vai em campo próprio.
		pn, lid := it.JID.ToNonAD(), types.JID{}
		if pn.Server == types.HiddenUserServer {
			lid = pn
			pn = it.PhoneNumber.ToNonAD()
		}
		if pn.IsEmpty() || pn.Server != types.DefaultUserServer {
			pn = types.NewJID(strings.TrimPrefix(phone, "+"), types.DefaultUserServer)
		}
		out["jid"] = pn.String()
		out["number"] = pn.User
		out["chatId"] = waChatID(pn) // alias WAHA
		out["lid"] = ""
		if !lid.IsEmpty() {
			out["lid"] = lid.String()
		}
	}
	if it.VerifiedName != nil && it.VerifiedName.Details != nil {
		out["verifiedName"] = it.VerifiedName.Details.GetVerifiedName()
	}
	writeJSON(w, http.StatusOK, out)
}

// GET /api/sessions/{sid}/contacts  → todos os contatos conhecidos
func (s *server) handleListContacts(w http.ResponseWriter, r *http.Request) {
	sess := s.pairedSession(w, r.PathValue("sid"))
	if sess == nil {
		return
	}
	all, err := sess.client.Store.Contacts.GetAllContacts(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	// o whatsmeow grava o mesmo contato na chave PN e na @lid: mapeia o LID
	// para o telefone e deduplica, para jid/number nunca virarem dígitos do LID.
	type entry struct {
		pn, lid types.JID
		c       types.ContactInfo
	}
	byKey := map[string]*entry{}
	order := []string{}
	merge := func(dst *types.ContactInfo, src types.ContactInfo) {
		if dst.FullName == "" {
			dst.FullName = src.FullName
		}
		if dst.FirstName == "" {
			dst.FirstName = src.FirstName
		}
		if dst.PushName == "" {
			dst.PushName = src.PushName
		}
		if dst.BusinessName == "" {
			dst.BusinessName = src.BusinessName
		}
	}
	for jid, c := range all {
		var pn, lid types.JID
		switch jid.Server {
		case types.HiddenUserServer:
			lid = jid
			if p, ok := sess.resolvedPhone(jid); ok {
				pn = types.NewJID(p, types.DefaultUserServer)
			}
		case types.DefaultUserServer:
			pn = jid
		default:
			continue
		}
		key := "lid:" + lid.User
		if !pn.IsEmpty() {
			key = "pn:" + pn.User
		}
		e, ok := byKey[key]
		if !ok {
			e = &entry{pn: pn, lid: lid, c: c}
			byKey[key] = e
			order = append(order, key)
			continue
		}
		if e.lid.IsEmpty() {
			e.lid = lid
		}
		merge(&e.c, c)
	}
	out := make([]map[string]any, 0, len(order))
	for _, k := range order {
		e := byKey[k]
		if e.lid.IsEmpty() && !e.pn.IsEmpty() {
			if l, err := sess.client.Store.LIDs.GetLIDForPN(r.Context(), e.pn); err == nil {
				e.lid = l
			}
		}
		main := e.pn
		if main.IsEmpty() {
			main = e.lid
		}
		row := map[string]any{
			"jid":          main.String(),
			"number":       main.User, // telefone; só cai no LID quando não há telefone (compat)
			"phone":        e.pn.User, // SÓ telefone real; "" se o LID não resolver
			"lid":          "",
			"fullName":     e.c.FullName,
			"firstName":    e.c.FirstName,
			"pushName":     e.c.PushName,
			"businessName": e.c.BusinessName,
			// aliases WAHA
			"id":       waChatID(main),
			"name":     firstNonEmptyOf(e.c.FullName, e.c.PushName, e.c.FirstName, e.c.BusinessName),
			"pushname": e.c.PushName,
		}
		if !e.lid.IsEmpty() {
			row["lid"] = e.lid.String()
		}
		out = append(out, row)
	}
	writeJSON(w, http.StatusOK, out)
}

// GET /api/sessions/{sid}/contacts/{jid}  → info (status/about, foto, devices)
func (s *server) handleContactInfo(w http.ResponseWriter, r *http.Request) {
	sess := s.pairedSession(w, r.PathValue("sid"))
	if sess == nil {
		return
	}
	jid, err := resolveRecipient(r.PathValue("jid"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	// identidade canônica: telefone (com/sem 9º dígito como o WhatsApp registrou)
	var pn, lid types.JID
	switch jid.Server {
	case types.HiddenUserServer:
		lid = jid
		if p, ok := sess.resolvedPhone(jid); ok {
			pn = types.NewJID(p, types.DefaultUserServer)
		}
	case types.DefaultUserServer:
		pn = jid
		if l, p, ok := sess.resolveCanonical(r.Context(), jid); ok {
			lid = l
			if p.User != "" {
				pn = p
			}
		}
	}
	query := jid
	if !pn.IsEmpty() {
		query = pn
	}
	info, err := sess.client.GetUserInfo(r.Context(), []types.JID{query})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	ui, ok := info[query]
	if !ok {
		// o WhatsApp pode indexar a resposta pelo outro JID (LID/PN)
		for _, v := range info {
			ui, ok = v, true
			break
		}
	}
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	if lid.IsEmpty() && !ui.LID.IsEmpty() {
		lid = ui.LID
	}
	main := pn
	if main.IsEmpty() {
		main = jid
	}
	out := map[string]any{
		"jid":       main.String(),
		"number":    main.User,
		"phone":     pn.User,
		"lid":       "",
		"status":    ui.Status,
		"pictureId": ui.PictureID,
		"id":        waChatID(main), // alias WAHA
	}
	if !lid.IsEmpty() {
		out["lid"] = lid.ToNonAD().String()
	}
	writeJSON(w, http.StatusOK, out)
}

// GET /api/sessions/{sid}/contacts/{jid}/picture?preview=true  → URL da foto
func (s *server) handleContactPicture(w http.ResponseWriter, r *http.Request) {
	sess := s.pairedSession(w, r.PathValue("sid"))
	if sess == nil {
		return
	}
	jid, err := resolveRecipient(r.PathValue("jid"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	pic, err := sess.client.GetProfilePictureInfo(r.Context(), jid, &whatsmeow.GetProfilePictureParams{
		Preview: r.URL.Query().Get("preview") == "true",
	})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if pic == nil {
		writeJSON(w, http.StatusOK, map[string]any{"url": "", "profilePictureURL": ""})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"url": pic.URL, "id": pic.ID, "type": pic.Type,
		"profilePictureURL": pic.URL, // alias WAHA
	})
}

// POST /api/sessions/{sid}/contacts/{jid}/block  e /unblock
func (s *server) handleBlock(block bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sess := s.pairedSession(w, r.PathValue("sid"))
		if sess == nil {
			return
		}
		jid, err := resolveRecipient(r.PathValue("jid"))
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		action := events.BlocklistChangeActionUnblock
		if block {
			action = events.BlocklistChangeActionBlock
		}
		if _, err := sess.client.UpdateBlocklist(r.Context(), jid, action); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "blocked": block})
	}
}

// GET /api/sessions/{sid}/blocklist  → JIDs bloqueados
func (s *server) handleBlocklist(w http.ResponseWriter, r *http.Request) {
	sess := s.pairedSession(w, r.PathValue("sid"))
	if sess == nil {
		return
	}
	bl, err := sess.client.GetBlocklist(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	jids := make([]string, 0, len(bl.JIDs))
	for _, j := range bl.JIDs {
		jids = append(jids, j.String())
	}
	writeJSON(w, http.StatusOK, map[string]any{"blocked": jids})
}

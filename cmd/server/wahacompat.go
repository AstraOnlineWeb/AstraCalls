package main

import "go.mau.fi/whatsmeow/types"

// waChatID converte um JID para o formato de "chatId" que clientes já feitos
// para outras HTTP APIs de WhatsApp esperam: usuários viram <numero>@c.us e
// grupos <id>@g.us. Mantém os demais servidores (newsletter, broadcast) como
// estão. É só um alias de compatibilidade — nossas respostas continuam trazendo
// o JID canônico (@s.whatsapp.net) no campo original.
func waChatID(j types.JID) string {
	switch j.Server {
	case types.DefaultUserServer:
		return j.User + "@c.us"
	default:
		return j.String()
	}
}

// waChatIDStr faz o mesmo a partir de um JID em string; se não parsear, devolve
// a string original.
func waChatIDStr(s string) string {
	j, err := types.ParseJID(s)
	if err != nil {
		return s
	}
	return waChatID(j)
}

// waRole traduz as flags de admin em um papel textual no estilo das APIs de
// referência (participant/admin/superadmin).
func waRole(isAdmin, isSuperAdmin bool) string {
	switch {
	case isSuperAdmin:
		return "superadmin"
	case isAdmin:
		return "admin"
	default:
		return "participant"
	}
}

// firstNonEmptyOf devolve o primeiro valor não-vazio (usado para montar um
// "name" de contato a partir de fullName/pushName/firstName).
func firstNonEmptyOf(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

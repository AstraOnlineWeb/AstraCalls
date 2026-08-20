package main

import "testing"

// %PDF-... : cabeçalho real de um PDF, o que http.DetectContentType fareja.
var pdfBytes = []byte("%PDF-1.4\n1 0 obj\n<<>>\nendobj\n")

func TestResolveDocMime(t *testing.T) {
	// hint do Chatwoot vence tudo
	if got := resolveDocMime("application/pdf", "arquivo", "", nil); got != "application/pdf" {
		t.Errorf("hint pdf = %q", got)
	}
	// hint genérico é ignorado; usa extensão do nome
	if got := resolveDocMime("application/octet-stream", "proposta.pdf", "", nil); got != "application/pdf" {
		t.Errorf("nome pdf = %q", got)
	}
	// sem hint e sem extensão no nome: usa Content-Type do download
	if got := resolveDocMime("", "blob", "application/pdf; charset=binary", nil); got != "application/pdf" {
		t.Errorf("http ct pdf = %q", got)
	}
	// nada disso: fareja os bytes (%PDF)
	if got := resolveDocMime("", "blob", "application/octet-stream", pdfBytes); got != "application/pdf" {
		t.Errorf("sniff pdf = %q", got)
	}
	// realmente desconhecido: cai no genérico
	if got := resolveDocMime("", "blob", "", []byte{0x01, 0x02, 0x03}); got != "application/octet-stream" {
		t.Errorf("desconhecido = %q", got)
	}
}

func TestIsGenericMime(t *testing.T) {
	for _, m := range []string{"", "application/octet-stream", "APPLICATION/OCTET-STREAM", "  "} {
		if !isGenericMime(m) {
			t.Errorf("isGenericMime(%q) = false, want true", m)
		}
	}
	if isGenericMime("application/pdf") {
		t.Error("application/pdf não é genérico")
	}
}

func TestFileNameFromURL(t *testing.T) {
	cases := map[string]string{
		"https://x/y/proposta-comercial.pdf":            "proposta-comercial.pdf",
		"https://x/y/proposta.pdf?token=abc&exp=123":    "proposta.pdf",
		"https://x/y/nota%20fiscal.pdf#frag":            "nota fiscal.pdf",
		"https://x/rails/active_storage/blobs/doc.xlsx": "doc.xlsx",
		"https://x/no-extension":                        "no-extension",
	}
	for in, want := range cases {
		if got := fileNameFromURL(in); got != want {
			t.Errorf("fileNameFromURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestMimeByFileName(t *testing.T) {
	if got := mimeByFileName("a.pdf"); got != "application/pdf" {
		t.Errorf("pdf mime = %q", got)
	}
	if got := mimeByFileName("noext"); got != "" {
		t.Errorf("noext should be empty, got %q", got)
	}
}

func TestEnsureFileExt(t *testing.T) {
	// já tem extensão: mantém
	if got := ensureFileExt("proposta.pdf", "application/pdf"); got != "proposta.pdf" {
		t.Errorf("keep ext = %q", got)
	}
	// sem extensão: deriva do mimetype
	if got := ensureFileExt("proposta", "application/pdf"); got != "proposta.pdf" {
		t.Errorf("derive ext = %q, want proposta.pdf", got)
	}
	// nome vazio + mimetype desconhecido: pelo menos não fica vazio
	if got := ensureFileExt("", "application/x-desconhecido"); got == "" {
		t.Errorf("empty name should get a fallback")
	}
}

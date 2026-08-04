package main

import "testing"

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

package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.mau.fi/whatsmeow/types"
)

// TestDirectIdentity cobre os ramos de directIdentity que não dependem do store
// do whatsmeow: número já é PN, e LID com alt (PN) fornecido pelo evento.
func TestDirectIdentity(t *testing.T) {
	s := &Session{}

	// 1:1 já com telefone (PN): identidade direta, sem altID.
	pn := types.NewJID("5567999998888", types.DefaultUserServer)
	phone, chatID, altID, resolved := s.directIdentity(pn, types.EmptyJID)
	if !resolved || phone != "5567999998888" || chatID != "5567999998888@s.whatsapp.net" || altID != "" {
		t.Fatalf("PN: phone=%q chatID=%q altID=%q resolved=%v", phone, chatID, altID, resolved)
	}

	// LID com alt (PN) vindo do evento: usa o PN como telefone e guarda o @lid como altID.
	lid := types.NewJID("192837465", types.HiddenUserServer)
	alt := types.NewJID("5567999998888", types.DefaultUserServer)
	phone, chatID, altID, resolved = s.directIdentity(lid, alt)
	if !resolved || phone != "5567999998888" || chatID != "5567999998888@s.whatsapp.net" || altID != lid.String() {
		t.Fatalf("LID+alt: phone=%q chatID=%q altID=%q resolved=%v", phone, chatID, altID, resolved)
	}
}

// TestEnsureContactBackfill: um contato criado antes de o número resolver (achado
// pelo identifier @lid, sem phone_number) recebe o backfill do telefone real, em
// vez de virar um contato duplicado.
func TestEnsureContactBackfill(t *testing.T) {
	lid := "192837465@lid"
	var backfilled string
	var searchQueries []string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/contacts/search"):
			q := r.URL.Query().Get("q")
			searchQueries = append(searchQueries, q)
			// busca pelo telefone não acha nada; busca pelo @lid acha o contato sem número.
			if q == lid {
				writeTestJSON(w, map[string]any{"payload": []any{map[string]any{
					"id":              42,
					"identifier":      lid,
					"phone_number":    nil,
					"contact_inboxes": []any{map[string]any{"inbox": map[string]any{"id": 7}, "source_id": "src-42"}},
				}}})
				return
			}
			writeTestJSON(w, map[string]any{"payload": []any{}})
		case r.Method == http.MethodPut && strings.HasSuffix(r.URL.Path, "/contacts/42"):
			var body map[string]any
			b, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(b, &body)
			backfilled, _ = body["phone_number"].(string)
			writeTestJSON(w, map[string]any{"payload": map[string]any{"id": 42}})
		default:
			t.Errorf("chamada inesperada: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	cfg := ChatwootConfig{URL: srv.URL, AccountID: 1, AccountToken: "tok", InboxID: 7}
	id, sid, err := cfg.ensureContact("5567999998888@s.whatsapp.net", "5567999998888", "Fulano", "", lid)
	if err != nil {
		t.Fatalf("ensureContact erro: %v", err)
	}
	if id != 42 || sid != "src-42" {
		t.Fatalf("achou contato errado: id=%d sid=%q", id, sid)
	}
	if backfilled != "+5567999998888" {
		t.Fatalf("backfill do telefone não aconteceu (got %q)", backfilled)
	}
	// tem que ter buscado pelo telefone E pelo altID (@lid).
	if len(searchQueries) < 2 {
		t.Fatalf("esperava busca por telefone e por altID, veio: %v", searchQueries)
	}
}

// TestEnsureContactNoBackfillWhenCorrect: contato já com o telefone certo não
// dispara PUT de backfill.
func TestEnsureContactNoBackfillWhenCorrect(t *testing.T) {
	var putCalled bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			putCalled = true
		}
		if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/contacts/search") {
			writeTestJSON(w, map[string]any{"payload": []any{map[string]any{
				"id":              9,
				"identifier":      "5567999998888@s.whatsapp.net",
				"phone_number":    "+5567999998888",
				"contact_inboxes": []any{map[string]any{"inbox": map[string]any{"id": 7}, "source_id": "src-9"}},
			}}})
			return
		}
		writeTestJSON(w, map[string]any{"payload": map[string]any{}})
	}))
	defer srv.Close()

	cfg := ChatwootConfig{URL: srv.URL, AccountID: 1, AccountToken: "tok", InboxID: 7}
	id, _, err := cfg.ensureContact("5567999998888@s.whatsapp.net", "5567999998888", "Fulano", "")
	if err != nil || id != 9 {
		t.Fatalf("ensureContact: id=%d err=%v", id, err)
	}
	if putCalled {
		t.Fatal("não deveria ter feito backfill (telefone já estava certo)")
	}
}

func TestEnsureContactBrazilianNinthDigitAlias(t *testing.T) {
	tests := []struct {
		name             string
		incomingPhone    string
		existingPhone    string
		expectedBackfill string
	}{
		{
			name:             "resposta sem nono digito encontra contato com nono digito",
			incomingPhone:    "551187654321",
			existingPhone:    "5511987654321",
			expectedBackfill: "",
		},
		{
			name:             "resposta com nono digito encontra contato sem nono digito",
			incomingPhone:    "5511987654321",
			existingPhone:    "551187654321",
			expectedBackfill: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var contactCreated bool
			var backfilledPhone string
			var searchQueries []string

			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/contacts/search"):
					query := r.URL.Query().Get("q")
					searchQueries = append(searchQueries, query)

					if query == tt.existingPhone {
						writeTestJSON(w, map[string]any{
							"payload": []any{
								map[string]any{
									"id":           42,
									"identifier":   tt.existingPhone + "@s.whatsapp.net",
									"phone_number": "+" + tt.existingPhone,
									"contact_inboxes": []any{
										map[string]any{
											"inbox":     map[string]any{"id": 7},
											"source_id": "src-42",
										},
									},
								},
							},
						})
						return
					}

					writeTestJSON(w, map[string]any{"payload": []any{}})

				case r.Method == http.MethodPut && strings.HasSuffix(r.URL.Path, "/contacts/42"):
					var body map[string]any
					data, _ := io.ReadAll(r.Body)
					_ = json.Unmarshal(data, &body)
					backfilledPhone, _ = body["phone_number"].(string)

					writeTestJSON(w, map[string]any{
						"payload": map[string]any{"id": 42},
					})

				case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/contacts"):
					contactCreated = true

					writeTestJSON(w, map[string]any{
						"payload": map[string]any{
							"contact": map[string]any{
								"id": 99,
								"contact_inboxes": []any{
									map[string]any{
										"inbox":     map[string]any{"id": 7},
										"source_id": "src-99",
									},
								},
							},
						},
					})

				default:
					t.Errorf("chamada inesperada: %s %s", r.Method, r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer srv.Close()

			cfg := ChatwootConfig{
				URL:          srv.URL,
				AccountID:    1,
				AccountToken: "tok",
				InboxID:      7,
			}

			chatID := tt.incomingPhone + "@s.whatsapp.net"

			id, sourceID, err := cfg.ensureContact(
				chatID,
				tt.incomingPhone,
				"Cliente",
				"",
			)

			if err != nil {
				t.Fatalf("ensureContact retornou erro: %v", err)
			}

			if id != 42 || sourceID != "src-42" {
				t.Fatalf(
					"deveria reutilizar contato 42, mas retornou id=%d sourceID=%q; buscas=%v",
					id,
					sourceID,
					searchQueries,
				)
			}

			if contactCreated {
				t.Fatal("não deveria criar outro contato para a variação brasileira do mesmo número")
			}

			if backfilledPhone != tt.expectedBackfill {
				t.Fatalf(
					"backfill inesperado: recebido=%q esperado=%q",
					backfilledPhone,
					tt.expectedBackfill,
				)
			}
		})
	}
}

func TestBrazilPhoneAliasesScope(t *testing.T) {
	tests := []struct {
		name     string
		phone    string
		expected string
	}{
		{
			name:     "celular brasileiro sem nono digito",
			phone:    "551187654321",
			expected: "551187654321,5511987654321",
		},
		{
			name:     "celular brasileiro com nono digito",
			phone:    "5511987654321",
			expected: "5511987654321,551187654321",
		},
		{
			name:     "telefone fixo brasileiro permanece inalterado",
			phone:    "551132345678",
			expected: "551132345678",
		},
		{
			name:     "telefone internacional permanece inalterado",
			phone:    "14155552671",
			expected: "14155552671",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := strings.Join(brazilPhoneAliases(tt.phone), ",")

			if got != tt.expected {
				t.Fatalf(
					"aliases inesperados para %q: recebido=%q esperado=%q",
					tt.phone,
					got,
					tt.expected,
				)
			}
		})
	}
}

func writeTestJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

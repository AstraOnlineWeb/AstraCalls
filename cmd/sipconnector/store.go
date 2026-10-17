package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// Linha SIP = uma caixa do Botfy ligada a um PABX. O Botfy é a fonte da verdade
// (empurra a config com PUT /api/lines/{id} a cada salvamento); aqui só guardamos
// para sobreviver a um restart sem depender de o Botfy reenviar.
const (
	modeRegisterIn  = "register_in"  // o PABX registra no conector (tronco)
	modeRegisterOut = "register_out" // o conector registra como ramal no PABX
)

// AuthUser é o usuário do DIGEST quando o PABX separa ramal e login (no 3CX é o
// "ID de autenticação"). Vazio = autentica com o próprio ramal/usuário.
type Agent struct {
	Email     string `json:"email"`
	Extension string `json:"extension"`
	AuthUser  string `json:"authUser"`
	Password  string `json:"password,omitempty"`
}

type Line struct {
	ID        string  `json:"id"`
	AccountID int     `json:"accountId"`
	InboxID   int     `json:"inboxId"`
	Name      string  `json:"name"`
	Enabled   bool    `json:"enabled"`
	Mode      string  `json:"mode"`
	Host      string  `json:"host"`
	Port      int     `json:"port"`
	Transport string  `json:"transport"`
	Proxy     string  `json:"proxy"`
	User      string  `json:"user"`
	AuthUser  string  `json:"authUser"`
	Password  string  `json:"password,omitempty"`
	DID       string  `json:"did"`
	Agents    []Agent `json:"agents"`
	UpdatedAt int64   `json:"updatedAt"`
}

var lineIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// normalize preenche padrões e valida. Senha vazia no PUT mantém a anterior (o
// Botfy nunca recebe a senha de volta, então não tem como reenviá-la).
func (l *Line) normalize(prev *Line) error {
	if !lineIDPattern.MatchString(l.ID) {
		return errors.New("id inválido (use letras, números, _ ou -)")
	}
	if l.AccountID <= 0 {
		return errors.New("accountId é obrigatório")
	}
	l.Mode = strings.TrimSpace(l.Mode)
	if l.Mode == "" {
		l.Mode = modeRegisterOut
	}
	if l.Mode != modeRegisterIn && l.Mode != modeRegisterOut {
		return errors.New("mode deve ser register_in ou register_out")
	}
	l.Host = strings.TrimSpace(l.Host)
	l.User = strings.TrimSpace(l.User)
	l.AuthUser = strings.TrimSpace(l.AuthUser)
	l.Proxy = strings.TrimSpace(l.Proxy)
	l.DID = strings.TrimSpace(l.DID)
	if l.Port <= 0 {
		l.Port = 5060
	}
	l.Transport = strings.ToLower(strings.TrimSpace(l.Transport))
	if l.Transport != "tcp" {
		l.Transport = "udp"
	}
	if prev != nil && l.Password == "" {
		l.Password = prev.Password
	}
	if l.User == "" {
		return errors.New("usuário SIP da linha é obrigatório")
	}
	if l.Mode == modeRegisterOut && l.Host == "" {
		return errors.New("servidor (host) do PABX é obrigatório para registrar no PABX")
	}
	if l.Mode == modeRegisterIn && l.Password == "" {
		return errors.New("senha é obrigatória para o PABX registrar no conector")
	}

	senhaAnterior := map[string]string{}
	if prev != nil {
		for _, a := range prev.Agents {
			senhaAnterior[a.Extension] = a.Password
		}
	}
	vistos := map[string]bool{}
	agentes := make([]Agent, 0, len(l.Agents))
	for _, a := range l.Agents {
		a.Email = strings.ToLower(strings.TrimSpace(a.Email))
		a.Extension = strings.TrimSpace(a.Extension)
		a.AuthUser = strings.TrimSpace(a.AuthUser)
		if a.Email == "" || a.Extension == "" {
			continue
		}
		if vistos[a.Extension] {
			return fmt.Errorf("ramal %s repetido", a.Extension)
		}
		vistos[a.Extension] = true
		if a.Password == "" {
			a.Password = senhaAnterior[a.Extension]
		}
		agentes = append(agentes, a)
	}
	// os ramais dos agentes registram no MESMO PABX da linha
	if len(agentes) > 0 && l.Host == "" {
		return errors.New("informe o servidor (host) do PABX para registrar os ramais dos agentes")
	}
	l.Agents = agentes
	l.UpdatedAt = time.Now().Unix()
	return nil
}

// authOr: usuário do digest — o ID de autenticação quando há, senão o próprio usuário.
func authOr(authUser, user string) string {
	if authUser != "" {
		return authUser
	}
	return user
}

func (l Line) agentByEmail(email string) *Agent {
	email = strings.ToLower(strings.TrimSpace(email))
	for i := range l.Agents {
		if l.Agents[i].Email == email {
			return &l.Agents[i]
		}
	}
	return nil
}

func (l Line) agentByExtension(ext string) *Agent {
	for i := range l.Agents {
		if l.Agents[i].Extension == ext {
			return &l.Agents[i]
		}
	}
	return nil
}

// public é o que a API devolve: nunca as senhas.
func (l Line) public() map[string]any {
	agentes := make([]map[string]any, 0, len(l.Agents))
	for _, a := range l.Agents {
		agentes = append(agentes, map[string]any{
			"email": a.Email, "extension": a.Extension, "authUser": a.AuthUser, "hasPassword": a.Password != "",
		})
	}
	return map[string]any{
		"id": l.ID, "accountId": l.AccountID, "inboxId": l.InboxID, "name": l.Name,
		"enabled": l.Enabled, "mode": l.Mode, "host": l.Host, "port": l.Port,
		"transport": l.Transport, "proxy": l.Proxy, "user": l.User, "authUser": l.AuthUser,
		"hasPassword": l.Password != "", "did": l.DID, "agents": agentes,
	}
}

type store struct {
	mu    sync.RWMutex
	path  string
	lines map[string]Line
}

func openStore(dir string) (*store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	s := &store{path: filepath.Join(dir, "lines.json"), lines: map[string]Line{}}
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	var list []Line
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, fmt.Errorf("lines.json corrompido: %w", err)
	}
	for _, l := range list {
		s.lines[l.ID] = l
	}
	return s, nil
}

func (s *store) all() []Line {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Line, 0, len(s.lines))
	for _, l := range s.lines {
		out = append(out, l)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func (s *store) get(id string) (Line, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	l, ok := s.lines[id]
	return l, ok
}

func (s *store) put(l Line) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lines[l.ID] = l
	return s.saveLocked()
}

func (s *store) delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.lines, id)
	return s.saveLocked()
}

// grava em arquivo temporário e renomeia: um crash no meio não corrompe o atual
func (s *store) saveLocked() error {
	list := make([]Line, 0, len(s.lines))
	for _, l := range s.lines {
		list = append(list, l)
	}
	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

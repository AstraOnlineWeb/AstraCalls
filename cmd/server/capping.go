package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Limite de envio para conversas NOVAS (message capping): o WhatsApp dá ao número
// uma cota de conversas iniciadas por ciclo; perto do fim manda avisos
// (FIRST_WARNING/SECOND_WARNING) e, esgotada, trava (CAPPED). É a mesma consulta
// MEX do WhatsApp Web (WAWebMexFetchNewChatMessageCappingInfoJobQuery). O push
// MessageCappingInfoNotification o whatsmeow descarta, então consultamos ao
// conectar, após aviso de restrição, a cada cappingRefreshInterval e sob demanda.

const (
	// ID atual do WhatsApp Web (out/2026) e o anterior, como fallback.
	cappingQueryID       = "27910975521856601"
	cappingQueryType     = "INDIVIDUAL_NEW_CHAT_THREAD"
	cappingLegacyQueryID = "24503548349331633"
	cappingLegacyType    = "INDIVIDUAL_NEW_CHAT_MSG"

	cappingRefreshInterval = 6 * time.Hour
	cappingMaxAge          = 30 * time.Minute // GET sem ?refresh reaproveita
)

// messageCapping é o estado devolvido pelo WhatsApp (campos já normalizados).
type messageCapping struct {
	TotalQuota         int    `json:"total_quota"` // -1 = sem limite aplicado à conta
	UsedQuota          int    `json:"used_quota"`
	Remaining          int    `json:"remaining"` // -1 quando unlimited
	Unlimited          bool   `json:"unlimited"`
	Status             string `json:"capping_status"` // NONE | FIRST_WARNING | SECOND_WARNING | CAPPED
	OTEStatus          string `json:"ote_status,omitempty"`
	MVStatus           string `json:"mv_status,omitempty"`
	CycleStartAt       int64  `json:"cycle_start_at"` // epoch ms
	CycleEndAt         int64  `json:"cycle_end_at"`   // epoch ms
	SecondsToCycleEnd  int64  `json:"seconds_to_cycle_end"`
	SubscriptionStatus string `json:"subscription_status,omitempty"`
}

// parseMessageCapping lê data.xwa2_message_capping_info. Números e timestamps
// podem vir como número ou string; timestamps em segundos ou ms.
func parseMessageCapping(data json.RawMessage) (*messageCapping, error) {
	var wrap struct {
		Info map[string]any `json:"xwa2_message_capping_info"`
	}
	if err := json.Unmarshal(data, &wrap); err != nil {
		return nil, err
	}
	if wrap.Info == nil {
		return nil, fmt.Errorf("resposta sem xwa2_message_capping_info")
	}
	m := wrap.Info
	c := &messageCapping{
		TotalQuota: int(anyInt(m["total_quota"])),
		UsedQuota:  int(anyInt(m["used_quota"])),
		Status:     anyStr(m["capping_status"]),
		OTEStatus:  anyStr(m["ote_status"]),
		MVStatus:   anyStr(m["mv_status"]),
	}
	if c.Status == "" {
		c.Status = "NONE"
	}
	if c.TotalQuota < 0 {
		c.Unlimited, c.Remaining = true, -1
	} else if c.Remaining = c.TotalQuota - c.UsedQuota; c.Remaining < 0 {
		c.Remaining = 0
	}
	c.CycleStartAt = epochMillis(anyInt(m["cycle_start_timestamp"]))
	c.CycleEndAt = epochMillis(anyInt(m["cycle_end_timestamp"]))
	if c.CycleEndAt > 0 {
		c.SecondsToCycleEnd = secondsRemaining(time.UnixMilli(c.CycleEndAt))
	}
	if sub, ok := m["subscription_status"].(map[string]any); ok {
		c.SubscriptionStatus = anyStr(sub["status"])
	}
	return c, nil
}

func anyInt(v any) int64 {
	switch x := v.(type) {
	case float64:
		return int64(x)
	case string:
		n, _ := strconv.ParseInt(strings.TrimSpace(x), 10, 64)
		return n
	}
	return 0
}

func anyStr(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

// epochMillis aceita segundos ou milissegundos.
func epochMillis(n int64) int64 {
	if n <= 0 {
		return 0
	}
	if n < 1e12 {
		return n * 1000
	}
	return n
}

// fetchMessageCapping consulta o WhatsApp e atualiza o cache da sessão.
func (s *Session) fetchMessageCapping(ctx context.Context) (*messageCapping, error) {
	if !s.client.IsConnected() || !s.client.IsLoggedIn() {
		return nil, fmt.Errorf("sessão não conectada")
	}
	in := s.client.DangerousInternals()
	data, err := in.SendMexIQ(ctx, cappingQueryID, map[string]any{"input": map[string]any{"type": cappingQueryType}})
	if err != nil {
		data, err = in.SendMexIQ(ctx, cappingLegacyQueryID, map[string]any{"input": map[string]any{"type": cappingLegacyType}})
	}
	if err != nil {
		s.mu.Lock()
		s.cappingErr = err.Error()
		s.mu.Unlock()
		return nil, err
	}
	c, err := parseMessageCapping(data)
	if err != nil {
		s.mu.Lock()
		s.cappingErr = err.Error()
		s.mu.Unlock()
		return nil, err
	}
	s.onMessageCapping(c)
	return c, nil
}

// onMessageCapping guarda o estado e, quando o status muda, avisa por webhook
// (message_capping) e — em aviso/travado — no Chatwoot, uma vez por status.
func (s *Session) onMessageCapping(c *messageCapping) {
	s.mu.Lock()
	prev := ""
	if s.capping != nil {
		prev = s.capping.Status
	}
	s.capping = c
	s.cappingAt = time.Now()
	s.cappingErr = ""
	alert := c.Status != "NONE" && c.Status != s.cappingAlerted
	if alert {
		s.cappingAlerted = c.Status
	} else if c.Status == "NONE" {
		s.cappingAlerted = ""
	}
	s.mu.Unlock()

	// 1ª consulta após subir com status normal não é mudança: não avisa.
	if prev == c.Status || (prev == "" && c.Status == "NONE") {
		return
	}
	s.log.Info("limite de conversas novas (message capping)", "status", c.Status,
		"used", c.UsedQuota, "total", c.TotalQuota, "cycle_end", c.CycleEndAt)
	s.dispatchWebhook("message_capping", map[string]any{
		"session":         s.id,
		"name":            s.name,
		"previous_status": prev,
		"capping":         c,
	})
	if alert {
		s.chatwootAlert(cappingAlertText(s.name, c))
	}
}

func cappingAlertText(name string, c *messageCapping) string {
	head := "⚠️ *AstraCalls — limite de conversas novas do WhatsApp*\n"
	when := ""
	if c.CycleEndAt > 0 {
		when = " até " + time.UnixMilli(c.CycleEndAt).Local().Format("02/01 15:04")
	}
	quota := ""
	if c.TotalQuota > 0 {
		quota = fmt.Sprintf(" (%d de %d usadas)", c.UsedQuota, c.TotalQuota)
	}
	if c.Status == "CAPPED" {
		return head + "A sessão *" + name + "* atingiu o limite de conversas novas" + quota + when +
			". Mensagens para quem ainda não conversou com o número não serão entregues. " +
			"Responder quem já falou com você continua liberado."
	}
	return head + "A sessão *" + name + "* está perto do limite de conversas novas" + quota + when +
		". Reduza os envios para contatos novos para não ser travada."
}

func (s *Session) cappingSnapshot() map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]any{"capping": s.capping, "updated_at": unixMillisOrZero(s.cappingAt)}
	if s.capping != nil && s.capping.CycleEndAt > 0 {
		c := *s.capping
		c.SecondsToCycleEnd = secondsRemaining(time.UnixMilli(c.CycleEndAt))
		out["capping"] = &c
	}
	if s.cappingErr != "" {
		out["error"] = s.cappingErr
	}
	return out
}

// refreshMessageCappingSoon consulta em segundo plano (após conectar ou aviso).
func (s *Session) refreshMessageCappingSoon(delay time.Duration) {
	go func() {
		select {
		case <-s.mgr.appCtx.Done():
			return
		case <-time.After(delay):
		}
		ctx, cancel := context.WithTimeout(s.mgr.appCtx, 20*time.Second)
		defer cancel()
		if _, err := s.fetchMessageCapping(ctx); err != nil {
			s.log.Debug("message capping: consulta falhou", "err", err)
		}
	}()
}

func (s *Session) runCappingRefresh() {
	ticker := time.NewTicker(cappingRefreshInterval)
	defer ticker.Stop()
	for {
		select {
		case <-s.mgr.appCtx.Done():
			return
		case <-ticker.C:
			if s.client.IsConnected() && s.client.IsLoggedIn() {
				s.refreshMessageCappingSoon(0)
			}
		}
	}
}

// handleMessageCapping: GET /api/sessions/{sid}/capping[?refresh=1]
func (s *server) handleMessageCapping(w http.ResponseWriter, r *http.Request) {
	sess := s.sessionByID(w, r.PathValue("sid"))
	if sess == nil {
		return
	}
	var out map[string]any
	var fetchErr error
	sess.mu.Lock()
	stale := sess.capping == nil || time.Since(sess.cappingAt) > cappingMaxAge
	sess.mu.Unlock()
	if q := r.URL.Query().Get("refresh"); q == "1" || q == "true" || stale {
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()
		_, fetchErr = sess.fetchMessageCapping(ctx)
	}
	out = sess.cappingSnapshot()
	if fetchErr != nil {
		out["error"] = fetchErr.Error()
	}
	out["session"] = sess.id
	writeJSON(w, http.StatusOK, out)
}

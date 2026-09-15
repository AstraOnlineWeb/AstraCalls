package main

import (
	"fmt"
	"net/http"
	"time"
)

// Observabilidade — endpoints ADITIVOS (fora de /api/, sem auth) p/ probes e
// scraping. Não alteram nada do comportamento existente.
//
//	GET /livez   -> liveness (sempre 200 enquanto o processo responde)
//	GET /readyz  -> readiness (200; hoje == liveness, o servidor só sobe pronto)
//	GET /metrics -> métricas em texto Prometheus (só contadores, sem dado sensível)
var processStart = time.Now()

func (s *server) handleLivez(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain")
	_, _ = w.Write([]byte("ok\n"))
}

func (s *server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	sessions := len(s.sessions.infos())
	dlq := whDeliver.totalDLQ()
	up := int64(time.Since(processStart).Seconds())
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	fmt.Fprint(w, "# HELP wacalls_sessions Número de sessões carregadas.\n")
	fmt.Fprint(w, "# TYPE wacalls_sessions gauge\n")
	fmt.Fprintf(w, "wacalls_sessions %d\n", sessions)
	fmt.Fprint(w, "# HELP wacalls_webhook_dlq_total Eventos de webhook na DLQ (todas as sessões).\n")
	fmt.Fprint(w, "# TYPE wacalls_webhook_dlq_total gauge\n")
	fmt.Fprintf(w, "wacalls_webhook_dlq_total %d\n", dlq)
	fmt.Fprint(w, "# HELP wacalls_uptime_seconds Tempo de processo em segundos.\n")
	fmt.Fprint(w, "# TYPE wacalls_uptime_seconds counter\n")
	fmt.Fprintf(w, "wacalls_uptime_seconds %d\n", up)
}

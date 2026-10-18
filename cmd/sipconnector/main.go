// Conector SIP do AstraCalls: liga um PABX IP qualquer ao AstraChat.
//
// Serviço SEPARADO do wacalls (não toca no núcleo do WhatsApp). Fala SIP/RTP com o
// PABX — nos dois modelos: o PABX registra aqui (tronco) ou o conector registra
// como ramal no PABX, inclusive um ramal por atendente — e expõe para o AstraChat a
// MESMA API de chamadas do wacalls: SSE /api/events, accept/reject/DELETE,
// /webrtc (navegador do atendente) e /ws pcm16 (agente de voz por IA).
package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

var version = "dev"

type server struct {
	cfg    config
	log    *slog.Logger
	store  *store
	broker *broker
	calls  *callManager
	ports  *portAllocator
	sip    *sipUA
}

func main() {
	log := slog.New(slog.NewTextHandler(os.Stdout, nil))
	cfg := loadConfig()
	if cfg.APIKey == "" {
		log.Error("SIPC_API_KEY é obrigatória")
		os.Exit(1)
	}
	st, err := openStore(cfg.DataDir)
	if err != nil {
		log.Error("não consegui abrir as linhas", "err", err)
		os.Exit(1)
	}
	s := &server{
		cfg: cfg, log: log, store: st, broker: newBroker(), calls: newCallManager(),
		ports: newPortAllocator(cfg.RTPMin, cfg.RTPMax),
	}
	if s.sip, err = newSipUA(s); err != nil {
		log.Error("SIP", "err", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	s.sip.start(ctx)
	for _, l := range st.all() {
		s.sip.applyLine(l)
	}

	httpSrv := &http.Server{Addr: cfg.HTTPAddr, Handler: s.routes(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		log.Info("API no ar", "addr", cfg.HTTPAddr, "version", version)
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Error("HTTP", "err", err)
			stop()
		}
	}()

	<-ctx.Done()
	log.Info("encerrando")
	for _, l := range st.all() {
		s.sip.removeLine(l.ID, true)
	}
	shutdown, done := context.WithTimeout(context.Background(), 5*time.Second)
	defer done()
	_ = httpSrv.Shutdown(shutdown)
}

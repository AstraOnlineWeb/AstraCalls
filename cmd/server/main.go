package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"
)

// envInt lê um inteiro de uma variável de ambiente (com valor padrão).
func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

// envStr lê uma string de uma variável de ambiente (com valor padrão).
func envStr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func main() {
	addr := flag.String("addr", ":8080", "HTTP listen address")
	// Storage: Postgres (1 banco por sessão, estilo WAHA). URL de manutenção em
	// WACALLS_PG_URL (ex.: postgres://user:pass@host:5432/postgres?sslmode=disable);
	// o usuário precisa de permissão CREATE DATABASE. WACALLS_PG_NAMESPACE = prefixo
	// dos bancos (default "wacalls" -> wacalls_main + wacalls_<id>).
	pgURL := flag.String("pg-url", os.Getenv("WACALLS_PG_URL"), "Postgres maintenance URL")
	pgNS := flag.String("pg-namespace", envStr("WACALLS_PG_NAMESPACE", "wacalls"), "prefix for per-session databases")
	staticDir := flag.String("static", "client/dist", "static client directory (optional)")
	debug := flag.Bool("debug", false, "verbose logging")
	// Padrão vem da env WACALLS_MAX_CALLS (fácil de editar na stack do Portainer);
	// a flag -max-calls-per-session ainda sobrescreve se passada.
	maxCalls := flag.Int("max-calls-per-session", envInt("WACALLS_MAX_CALLS", 8), "max concurrent calls per session (0 = unlimited)")
	flag.Parse()

	level := slog.LevelInfo
	if *debug {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
	slog.SetDefault(log)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	srv, err := newServer(ctx, *pgURL, *pgNS, *staticDir, *maxCalls, log)
	if err != nil {
		log.Error("startup failed", "err", err)
		os.Exit(1)
	}
	defer srv.sessions.disconnectAll()

	if err := srv.sessions.Restore(ctx); err != nil {
		log.Error("session restore failed", "err", err)
		os.Exit(1)
	}

	startRecordingJanitor(log)

	// Worker de reentrega ao Chatwoot: reenvia com backoff o que falhou (ex.:
	// Chatwoot fora do ar) em vez de perder a mensagem.
	go srv.sessions.runChatwootOutbox(ctx)

	httpSrv := &http.Server{Addr: *addr, Handler: srv.routes()}
	go func() {
		log.Info("HTTP server listening", "addr", *addr)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("http server error", "err", err)
		}
	}()

	<-ctx.Done()
	log.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = httpSrv.Shutdown(shutdownCtx)
}

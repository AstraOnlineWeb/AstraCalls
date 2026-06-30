package main

import (
	"context"
	"log/slog"
	"os"

	waLog "go.mau.fi/whatsmeow/util/log"
)

type server struct {
	broker    *Broker
	sessions  *SessionManager
	log       *slog.Logger
	staticDir string
	sipGW     *SIPGateway
}

// newServer monta o provedor de banco (Postgres, 1 banco por sessão no estilo
// WAHA), abre o banco principal e inicializa o gerenciador de sessões.
func newServer(ctx context.Context, pgURL, pgNamespace, staticDir string, maxCalls int, log *slog.Logger) (*server, error) {
	waLogger := waLog.Noop
	if log.Enabled(ctx, slog.LevelDebug) {
		waLogger = waLog.Stdout("WA", "DEBUG", true)
	}

	provider, err := newDBProvider(ctx, pgURL, pgNamespace, waLogger, log)
	if err != nil {
		return nil, err
	}

	mainDB, err := provider.openMainDB(ctx)
	if err != nil {
		return nil, err
	}
	store, err := newSessionStore(ctx, mainDB)
	if err != nil {
		return nil, err
	}

	broker := NewBroker()
	mgr := newSessionManager(ctx, provider, broker, store, waLogger, log, maxCalls)
	broker.SnapshotFn = mgr.snapshotEvents

	// Gateway SIP: o PBX/softphone do cliente se registra aqui (modelo Wavoip).
	// Endereço configurável por WACALLS_SIP_ADDR (padrão 0.0.0.0:5060).
	sipAddr := os.Getenv("WACALLS_SIP_ADDR")
	if sipAddr == "" {
		sipAddr = "0.0.0.0:5060"
	}
	sipGateway, err := NewSIPGateway(mgr, log)
	if err != nil {
		log.Error("SIP gateway init failed", "err", err)
	} else {
		mgr.sipInbound = sipGateway.handleInboundCall
		_ = sipGateway.Start(ctx, sipAddr)
	}

	return &server{broker: broker, sessions: mgr, log: log, staticDir: staticDir, sipGW: sipGateway}, nil
}

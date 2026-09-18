package app

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"malaka/internal/config"
	"malaka/internal/modules/iam"
	"malaka/internal/modules/tasks"
	"malaka/internal/platform/audit"
	"malaka/internal/platform/db"
	"malaka/internal/platform/event"
	"malaka/internal/platform/mid"
)

type App struct {
	cfg   *config.Config
	db    *db.DB
	bus   *event.Bus
	relay *event.OutboxRelay
	srv   *http.Server
}

func New(ctx context.Context) (*App, error) {
	cfg := config.Get()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	database, err := db.Open(db.Driver(cfg.DBDriver), cfg.DBURL, db.PoolConfig{
		MaxOpenConns:    cfg.DBMaxOpenConns,
		MaxIdleConns:    cfg.DBMaxIdleConns,
		ConnMaxLifetime: cfg.DBConnLifetime,
		SlowThreshold:   200 * time.Millisecond,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to initialize database pool: %w", err)
	}

	bus := event.NewBus(4)
	relay := event.NewOutboxRelay(database, bus, 500*time.Millisecond)

	mux := http.NewServeMux()

	mux.Handle("GET /healthz", mid.HealthLiveness())
	mux.Handle("GET /readyz", mid.HealthReadiness(database))
	mux.Handle("GET /metrics", mid.Metrics(bus.Dropped))

	auditWriter := audit.NewWriter(database)
	idempotencyMid := mid.Idempotency(database, cfg.IdempotencyTTL)

	tokenMgr := iam.NewTokenManager(cfg.JWTSecret, 15*time.Minute)
	iamStore := iam.NewStore(database)
	iamSvc := iam.NewService(database, iamStore, tokenMgr)
	authMid := mid.Authenticate(iamSvc)
	iamHdl := iam.NewHandler(iamSvc)
	iamHdl.RegisterRoutes(mux, authMid)

	tasksSvc := tasks.NewService(database, auditWriter)
	tasksHdl := tasks.NewHandler(tasksSvc)
	tasksHdl.RegisterRoutes(mux, authMid, idempotencyMid)

	rateLimit := mid.RateLimit(mid.RateLimitConfig{
		Rate:      20,
		Burst:     40,
		SkipPaths: []string{"/healthz", "/readyz", "/metrics"},
	})
	handler := mid.Chain(mux,
		mid.Correlation,
		mid.Recover,
		mid.SecurityHeaders(mid.DefaultSecurityHeaders()),
		mid.CORS(cfg.CORS),
		rateLimit,
		mid.BodyLimit(1<<20),
		mid.Logger,
	)

	srv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           handler,
		ReadTimeout:       10 * time.Second,
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	return &App{
		cfg:   cfg,
		db:    database,
		bus:   bus,
		relay: relay,
		srv:   srv,
	}, nil
}

func (a *App) Run(ctx context.Context) error {
	runCtx, cancelRun := context.WithCancel(ctx)
	defer cancelRun()

	relayDone := make(chan struct{})
	go func() {
		defer close(relayDone)
		if err := a.relay.Run(runCtx); err != nil && ctx.Err() == nil {
			slog.Error("outbox relay failed", "error_type", fmt.Sprintf("%T", err))
		}
	}()

	errCh := make(chan error, 1)
	go func() {
		slog.Info("starting HTTP server", "port", a.cfg.Port, "env", a.cfg.Env)
		if err := a.srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()

	select {
	case <-ctx.Done():
		slog.Info("shutdown signal received")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := a.srv.Shutdown(shutdownCtx); err != nil {
			slog.Error("http shutdown error", "error_type", fmt.Sprintf("%T", err))
		}
		<-relayDone
		a.close()
		return nil
	case err := <-errCh:
		cancelRun()
		<-relayDone
		a.close()
		return err
	}
}

func (a *App) close() {
	a.bus.Close()
	a.db.Close()
}

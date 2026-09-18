package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"malaka/internal/app"
	"malaka/internal/config"
	"malaka/internal/platform/mid"
	"malaka/pkg/slogx"
	"malaka/pkg/version"
)

func main() {
	cfg := config.Get()
	slogx.Init(slogx.Config{
		Level:     parseLogLevel(cfg.LogLevel),
		Format:    "json",
		AddSource: !strings.EqualFold(cfg.Env, "production"),
		Extractors: []slogx.ContextExtractor{
			slogx.ExtractorFunc(mid.LogAttrs),
		},
	})

	slog.Info("starting Malaka Task Management API", "version", version.Version)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	a, err := app.New(ctx)
	if err != nil {
		slog.Error("fatal initialization error", "error", err)
		os.Exit(1)
	}

	if err := a.Run(ctx); err != nil {
		slog.Error("server runtime error", "error", err)
		os.Exit(1)
	}
}

func parseLogLevel(value string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

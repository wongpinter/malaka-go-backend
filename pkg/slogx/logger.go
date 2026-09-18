package slogx

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
)

type Config struct {
	Level      slog.Level
	Format     string
	AddSource  bool
	Extractors []ContextExtractor
	Output     io.Writer
}

func New(cfg Config) *slog.Logger {
	out := cfg.Output
	if out == nil {
		out = os.Stdout
	}

	opts := &slog.HandlerOptions{
		Level:       cfg.Level,
		AddSource:   cfg.AddSource,
		ReplaceAttr: RedactAttr(nil, nil),
	}

	var inner slog.Handler
	if cfg.Format == "text" {
		inner = slog.NewTextHandler(out, opts)
	} else {
		inner = slog.NewJSONHandler(out, opts)
	}

	return slog.New(NewHandler(inner, cfg.Extractors...))
}

func Init(cfg Config) *slog.Logger {
	logger := New(cfg)
	slog.SetDefault(logger)
	return logger
}

func ErrorType(err error) slog.Attr {
	return slog.String("error_type", fmt.Sprintf("%T", err))
}

func Warn(ctx context.Context, msg string, attrs ...slog.Attr) {
	slog.Default().LogAttrs(ctx, slog.LevelWarn, msg, attrs...)
}

func Error(ctx context.Context, msg string, attrs ...slog.Attr) {
	slog.Default().LogAttrs(ctx, slog.LevelError, msg, attrs...)
}

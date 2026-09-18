package slogx

import (
	"context"
	"log/slog"
)

type ContextExtractor interface {
	Extract(ctx context.Context) []slog.Attr
}

type ExtractorFunc func(ctx context.Context) []slog.Attr

func (f ExtractorFunc) Extract(ctx context.Context) []slog.Attr {
	if f == nil {
		return nil
	}
	return f(ctx)
}

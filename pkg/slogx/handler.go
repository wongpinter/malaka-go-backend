package slogx

import (
	"context"
	"log/slog"
)

type Handler struct {
	inner      slog.Handler
	extractors []ContextExtractor
}

func NewHandler(inner slog.Handler, extractors ...ContextExtractor) *Handler {
	return &Handler{
		inner:      inner,
		extractors: extractors,
	}
}

func (h *Handler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.inner.Enabled(ctx, level)
}

func (h *Handler) Handle(ctx context.Context, r slog.Record) error {
	if ctx != nil && len(h.extractors) > 0 {
		var ctxAttrs []slog.Attr
		for _, ex := range h.extractors {
			if attrs := ex.Extract(ctx); len(attrs) > 0 {
				ctxAttrs = append(ctxAttrs, attrs...)
			}
		}

		if len(ctxAttrs) > 0 {
			r = r.Clone()
			r.AddAttrs(ctxAttrs...)
		}
	}

	return h.inner.Handle(ctx, r)
}

func (h *Handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &Handler{
		inner:      h.inner.WithAttrs(attrs),
		extractors: h.extractors,
	}
}

func (h *Handler) WithGroup(name string) slog.Handler {
	return &Handler{
		inner:      h.inner.WithGroup(name),
		extractors: h.extractors,
	}
}

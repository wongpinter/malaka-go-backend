package event

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"malaka/internal/platform/mid"
)

type Event struct {
	Type          string
	CorrelationID string
	UserID        int64
	Payload       any
}

type queuedEvent struct {
	event  Event
	result chan error
}

type Bus struct {
	ch        chan queuedEvent
	handlers  map[string][]func(context.Context, Event) error
	mu        sync.RWMutex
	publishMu sync.RWMutex
	closeOnce sync.Once
	closed    bool
	wg        sync.WaitGroup
	workers   int
	dropped   atomic.Int64
}

func NewBus(workers int) *Bus {
	if workers <= 0 {
		workers = 4
	}
	b := &Bus{
		ch:       make(chan queuedEvent, 1024),
		handlers: make(map[string][]func(context.Context, Event) error),
		workers:  workers,
	}
	b.wg.Add(workers)
	for i := 0; i < workers; i++ {
		go b.worker()
	}
	return b
}

func (b *Bus) Subscribe(eventType string, h func(context.Context, Event) error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.handlers[eventType] = append(b.handlers[eventType], h)
}

func (b *Bus) worker() {
	defer b.wg.Done()
	for queued := range b.ch {
		ev := queued.event
		b.mu.RLock()
		specific := b.handlers[ev.Type]
		wildcard := b.handlers["*"]
		targets := make([]func(context.Context, Event) error, 0, len(specific)+len(wildcard)+8)
		targets = append(targets, specific...)
		targets = append(targets, wildcard...)
		parts := strings.Split(ev.Type, ".")
		for i := len(parts) - 1; i >= 1; i-- {
			prefix := strings.Join(parts[:i], ".") + ".*"
			if matched := b.handlers[prefix]; len(matched) > 0 {
				targets = append(targets, matched...)
			}
		}
		b.mu.RUnlock()

		var firstErr error
		for _, handler := range targets {
			func() {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				if ev.CorrelationID != "" {
					ctx = mid.WithCorrelationID(ctx, ev.CorrelationID)
				}
				if ev.UserID != 0 {
					ctx = mid.WithUserID(ctx, ev.UserID)
				}
				var handlerErr error
				defer func() {
					if r := recover(); r != nil {
						handlerErr = errors.New("event handler panic")
						slog.ErrorContext(ctx, "event handler panic recovered", "type", ev.Type, "panic_type", fmt.Sprintf("%T", r))
					}
					if handlerErr != nil && firstErr == nil {
						firstErr = handlerErr
					}
				}()
				handlerErr = handler(ctx, ev)
				if handlerErr != nil {
					slog.WarnContext(ctx, "event handler returned error", "type", ev.Type, "error_type", fmt.Sprintf("%T", handlerErr))
				}
			}()
		}
		if queued.result != nil {
			queued.result <- firstErr
		}
	}
}

func (b *Bus) Publish(ctx context.Context, ev Event) bool {
	return b.enqueue(ctx, queuedEvent{event: ev})
}

func (b *Bus) PublishAndWait(ctx context.Context, ev Event) error {
	if ctx == nil {
		ctx = context.Background()
	}
	result := make(chan error, 1)
	if !b.enqueue(ctx, queuedEvent{event: ev, result: result}) {
		if err := ctx.Err(); err != nil {
			return err
		}
		return errors.New("event bus unavailable")
	}
	select {
	case err := <-result:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (b *Bus) enqueue(ctx context.Context, ev queuedEvent) bool {
	if ctx == nil {
		ctx = context.Background()
	}
	if ev.event.CorrelationID == "" {
		ev.event.CorrelationID = mid.GetCorrelationID(ctx)
	}
	if ev.event.UserID == 0 {
		ev.event.UserID = mid.GetUserID(ctx)
	}
	b.publishMu.RLock()
	defer b.publishMu.RUnlock()
	if b.closed {
		return false
	}
	select {
	case <-ctx.Done():
		return false
	case b.ch <- ev:
		return true
	default:
		count := b.dropped.Add(1)
		slog.WarnContext(ctx, "event bus saturated, rejecting event",
			"type", ev.event.Type,
			"event_bus_dropped_total", count,
		)
		return false
	}
}

func (b *Bus) Dropped() int64 { return b.dropped.Load() }

func (b *Bus) Close() {
	b.closeOnce.Do(func() {
		b.publishMu.Lock()
		b.closed = true
		close(b.ch)
		b.publishMu.Unlock()
	})
	done := make(chan struct{})
	go func() {
		b.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		slog.Info("event bus workers drained")
	case <-time.After(30 * time.Second):
		slog.Warn("event bus drain timed out")
	}
}

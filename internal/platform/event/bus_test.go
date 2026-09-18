package event_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"malaka/internal/platform/event"
	"malaka/internal/platform/mid"
)

func TestEventBus(t *testing.T) {
	bus := event.NewBus(2)
	defer bus.Close()

	var wg sync.WaitGroup
	wg.Add(1)

	var received event.Event
	bus.Subscribe("task.created", func(ctx context.Context, ev event.Event) error {
		received = ev
		wg.Done()
		return nil
	})

	bus.Publish(context.Background(), event.Event{
		Type:    "task.created",
		Payload: map[string]string{"title": "Belajar Go"},
	})

	c := make(chan struct{})
	go func() {
		wg.Wait()
		close(c)
	}()

	select {
	case <-c:
		if received.Type != "task.created" {
			t.Fatalf("unexpected received event: %+v", received)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for event bus handler")
	}
}

func TestEventBus_ContextPropagation(t *testing.T) {
	bus := event.NewBus(2)
	defer bus.Close()

	var wg sync.WaitGroup
	wg.Add(1)

	var (
		receivedCid string
		receivedUID int64
	)

	bus.Subscribe("task.assigned", func(ctx context.Context, ev event.Event) error {
		receivedCid = mid.GetCorrelationID(ctx)
		receivedUID = mid.GetUserID(ctx)
		wg.Done()
		return nil
	})

	ctx := context.Background()
	ctx = mid.WithCorrelationID(ctx, "req-cid-9999")
	ctx = mid.WithUserID(ctx, 505)

	bus.Publish(ctx, event.Event{
		Type:    "task.assigned",
		Payload: map[string]string{"action": "assign"},
	})

	c := make(chan struct{})
	go func() {
		wg.Wait()
		close(c)
	}()

	select {
	case <-c:
		if receivedCid != "req-cid-9999" {
			t.Errorf("expected correlation ID 'req-cid-9999', got %q", receivedCid)
		}
		if receivedUID != 505 {
			t.Errorf("expected user ID 505, got %d", receivedUID)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for event handler")
	}
}

func TestEventBus_PublishAndWaitReturnsHandlerError(t *testing.T) {
	bus := event.NewBus(1)
	defer bus.Close()
	bus.Subscribe("will.fail", func(context.Context, event.Event) error {
		return context.DeadlineExceeded
	})

	if err := bus.PublishAndWait(context.Background(), event.Event{Type: "will.fail"}); err != context.DeadlineExceeded {
		t.Fatalf("expected handler error, got %v", err)
	}
}

func TestEventBus_PanicRecovery(t *testing.T) {
	bus := event.NewBus(2)
	defer bus.Close()

	var wg sync.WaitGroup
	wg.Add(2)

	bus.Subscribe("will.panic", func(ctx context.Context, ev event.Event) error {
		defer wg.Done()
		panic("intentional panic for test")
	})

	bus.Subscribe("will.succeed", func(ctx context.Context, ev event.Event) error {
		defer wg.Done()
		return nil
	})

	bus.Publish(context.Background(), event.Event{Type: "will.panic"})
	bus.Publish(context.Background(), event.Event{Type: "will.succeed"})

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for handlers after panic")
	}
}

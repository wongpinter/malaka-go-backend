package event_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"malaka/internal/platform/event"
	"malaka/internal/platform/mid"
	"malaka/internal/platform/testutil"
)

func TestOutbox_Add_And_Relay_ContextPropagation(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping database integration test in short mode")
	}

	testDB := testutil.NewTestDB(t)
	ctx := context.Background()

	var userID int64
	err := testDB.DB.Pool.QueryRowContext(ctx, `
		INSERT INTO iam.users (email, name, password_hash)
		VALUES ('worker-test@example.com', 'Worker Tester', 'seed-hash')
		RETURNING id
	`).Scan(&userID)
	if err != nil {
		t.Fatalf("failed to seed user: %v", err)
	}

	const expectedCID = "cid-outbox-relay-777"
	publishCtx := context.Background()
	publishCtx = mid.WithCorrelationID(publishCtx, expectedCID)
	publishCtx = mid.WithUserID(publishCtx, userID)

	tx, err := testDB.DB.Pool.BeginTx(publishCtx, nil)
	if err != nil {
		t.Fatalf("begin tx failed: %v", err)
	}

	err = event.Add(publishCtx, tx, "task.assigned", map[string]string{
		"assignee": "adit",
	})
	if err != nil {
		_ = tx.Rollback()
		t.Fatalf("event.Add failed: %v", err)
	}

	if err := tx.Commit(); err != nil {
		t.Fatalf("commit tx failed: %v", err)
	}

	var (
		dbCID    *string
		dbUID    *int64
		dbStatus string
	)
	err = testDB.DB.Pool.QueryRowContext(ctx, `
		SELECT correlation_id, user_id, status
		FROM public.outbox_events
		WHERE user_id = $1 AND event_type = 'task.assigned'
	`, userID).Scan(&dbCID, &dbUID, &dbStatus)
	if err != nil {
		t.Fatalf("failed to query inserted outbox event: %v", err)
	}

	if dbCID == nil || *dbCID != expectedCID {
		t.Fatalf("expected outbox correlation_id %q, got %v", expectedCID, dbCID)
	}
	if dbUID == nil || *dbUID != userID {
		t.Fatalf("expected outbox user_id %d, got %v", userID, dbUID)
	}
	if dbStatus != "pending" {
		t.Fatalf("expected outbox status 'pending', got %q", dbStatus)
	}

	bus := event.NewBus(2)
	defer bus.Close()

	var wg sync.WaitGroup
	wg.Add(1)

	var (
		receivedCID  string
		receivedUID  int64
		receivedType string
	)

	bus.Subscribe("task.assigned", func(handlerCtx context.Context, ev event.Event) error {
		receivedCID = mid.GetCorrelationID(handlerCtx)
		receivedUID = mid.GetUserID(handlerCtx)
		receivedType = ev.Type
		wg.Done()
		return nil
	})

	relay := event.NewOutboxRelay(testDB.DB, bus, 50*time.Millisecond)
	relayCtx, cancelRelay := context.WithCancel(context.Background())
	defer cancelRelay()

	go func() {
		_ = relay.Run(relayCtx)
	}()

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		if receivedType != "task.assigned" {
			t.Errorf("expected event type 'task.assigned', got %q", receivedType)
		}
		if receivedCID != expectedCID {
			t.Errorf("expected propagated correlation ID %q, got %q", expectedCID, receivedCID)
		}
		if receivedUID != userID {
			t.Errorf("expected propagated user ID %d, got %d", userID, receivedUID)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for relay to process and dispatch outbox event")
	}

	deadline := time.Now().Add(2 * time.Second)
	var finalStatus string
	for time.Now().Before(deadline) {
		err = testDB.DB.Pool.QueryRowContext(ctx, `
			SELECT status FROM public.outbox_events
			WHERE user_id = $1 AND event_type = 'task.assigned'
		`, userID).Scan(&finalStatus)
		if err == nil && finalStatus == "done" {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}

	cancelRelay()

	if finalStatus != "done" {
		t.Errorf("expected outbox status 'done', got %q", finalStatus)
	}
}

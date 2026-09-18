package event

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	platformdb "malaka/internal/platform/db"
	"malaka/internal/platform/mid"
)

func openOutboxDB(t *testing.T) *platformdb.DB {
	t.Helper()
	database, err := platformdb.OpenSQLite(t.TempDir()+"/outbox.db", platformdb.PoolConfig{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })

	ctx := context.Background()
	if _, err := database.ExecContext(ctx, `
		INSERT INTO users (public_id, email, name, password_hash, created_at, updated_at)
		VALUES (?, 'outbox@example.com', 'Outbox', 'hash', ?, ?)
	`, uuid.NewString(), platformdb.SQLiteTime(time.Now()), platformdb.SQLiteTime(time.Now())); err != nil {
		t.Fatal(err)
	}
	return database
}

func queueEvent(t *testing.T, database *platformdb.DB, eventType string, payload any) {
	t.Helper()
	ctx := mid.WithUserID(mid.WithCorrelationID(context.Background(), "cid-outbox-sqlite"), 1)
	if err := database.ExecTx(ctx, func(tx platformdb.Tx) error {
		return Add(ctx, tx, eventType, payload)
	}); err != nil {
		t.Fatal(err)
	}
}

type outboxRow struct {
	status        string
	attempts      int
	nextAttemptAt *string
	publishedAt   *string
	payload       string
}

func loadOutboxRow(t *testing.T, database *platformdb.DB) outboxRow {
	t.Helper()
	var row outboxRow
	if err := database.QueryRowContext(context.Background(),
		`SELECT status, attempts, next_attempt_at, published_at, payload FROM outbox_events`).
		Scan(&row.status, &row.attempts, &row.nextAttemptAt, &row.publishedAt, &row.payload); err != nil {
		t.Fatal(err)
	}
	return row
}

func TestOutboxSQLite_RelayPublishesAndMarksDone(t *testing.T) {
	database := openOutboxDB(t)
	bus := NewBus(1)
	t.Cleanup(bus.Close)

	received := make(chan Event, 1)
	bus.Subscribe("task.created", func(_ context.Context, ev Event) error {
		received <- ev
		return nil
	})

	queueEvent(t, database, "task.created", map[string]any{"task_id": "abc"})

	relay := NewOutboxRelay(database, bus, time.Millisecond)
	if err := relay.processBatch(context.Background()); err != nil {
		t.Fatal(err)
	}

	select {
	case ev := <-received:
		if ev.Type != "task.created" || ev.CorrelationID != "cid-outbox-sqlite" || ev.UserID != 1 {
			t.Fatalf("handler saw an incomplete event: %+v", ev)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("handler was never invoked")
	}

	row := loadOutboxRow(t, database)
	if row.status != "done" || row.publishedAt == nil {
		t.Fatalf("event was not marked done: %+v", row)
	}
	if row.attempts != 1 {
		t.Fatalf("want one attempt recorded, got %d", row.attempts)
	}

	if err := relay.processBatch(context.Background()); err != nil {
		t.Fatal(err)
	}
	if again := loadOutboxRow(t, database); again.attempts != 1 {
		t.Fatalf("done event was re-published: %+v", again)
	}
}

func TestOutboxSQLite_FailedDeliveryBacksOffThenDeadLetters(t *testing.T) {
	database := openOutboxDB(t)
	bus := NewBus(1)
	t.Cleanup(bus.Close)
	bus.Subscribe("task.updated", func(context.Context, Event) error {
		return errors.New("handler down")
	})

	relay := NewOutboxRelay(database, bus, time.Millisecond)
	ctx := context.Background()

	queueEvent(t, database, "task.updated", map[string]any{"task_id": "abc"})
	if err := relay.processBatch(ctx); err != nil {
		t.Fatal(err)
	}
	row := loadOutboxRow(t, database)
	if row.status != "failed" {
		t.Fatalf("want failed status, got %+v", row)
	}
	if row.nextAttemptAt == nil {
		t.Fatal("failed delivery must schedule a retry")
	}
	retryAt, err := platformdb.ParseSQLiteTime(*row.nextAttemptAt)
	if err != nil {
		t.Fatal(err)
	}
	if !retryAt.After(time.Now()) {
		t.Fatalf("retry must be scheduled in the future, got %s", *row.nextAttemptAt)
	}

	if _, err := database.ExecContext(ctx, `UPDATE outbox_events SET attempts = ?, next_attempt_at = ?`,
		maxOutboxAttempts-1, platformdb.SQLiteTime(time.Now())); err != nil {
		t.Fatal(err)
	}
	if err := relay.processBatch(ctx); err != nil {
		t.Fatal(err)
	}
	row = loadOutboxRow(t, database)
	if row.status != "dead" {
		t.Fatalf("want dead status after %d attempts, got %+v", maxOutboxAttempts, row)
	}
	if row.nextAttemptAt != nil {
		t.Fatalf("dead events must clear their retry time, got %q", *row.nextAttemptAt)
	}

	if err := relay.processBatch(ctx); err != nil {
		t.Fatal(err)
	}
	if again := loadOutboxRow(t, database); again.attempts != row.attempts {
		t.Fatalf("dead event was polled again: %+v", again)
	}
}

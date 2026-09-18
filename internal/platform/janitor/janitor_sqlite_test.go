package janitor_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"malaka/internal/platform/db"
	"malaka/internal/platform/janitor"
)

func TestJanitorSQLiteRunAll(t *testing.T) {
	database, err := db.OpenSQLite(t.TempDir()+"/janitor.db", db.PoolConfig{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })

	ctx := context.Background()
	now := time.Now()
	at := func(d time.Duration) string { return db.SQLiteTime(now.Add(d)) }

	if _, err := database.ExecContext(ctx, `
		INSERT INTO users (public_id, email, name, password_hash, created_at, updated_at)
		VALUES (?, 'janitor@example.com', 'Janitor', 'hash', ?, ?)
	`, uuid.NewString(), at(0), at(0)); err != nil {
		t.Fatal(err)
	}

	seed := []struct {
		name  string
		query string
		args  []any
	}{
		{"expired session", `INSERT INTO sessions (public_id, user_id, token_hash, expires_at, created_at) VALUES (?, 1, ?, ?, ?)`,
			[]any{uuid.NewString(), "expired", at(-time.Hour), at(-2 * time.Hour)}},
		{"stale revoked session", `INSERT INTO sessions (public_id, user_id, token_hash, expires_at, revoked_at, created_at) VALUES (?, 1, ?, ?, ?, ?)`,
			[]any{uuid.NewString(), "stale-revoked", at(24 * time.Hour), at(-8 * 24 * time.Hour), at(-9 * 24 * time.Hour)}},
		{"active session", `INSERT INTO sessions (public_id, user_id, token_hash, expires_at, created_at) VALUES (?, 1, ?, ?, ?)`,
			[]any{uuid.NewString(), "active", at(24 * time.Hour), at(0)}},
		{"expired completed key", `INSERT INTO idempotency_keys (idempotency_key, user_id, request_path, request_method, request_hash, status, expires_at, created_at) VALUES (?, 1, '/api/v1/tasks', 'POST', 'h', 'completed', ?, ?)`,
			[]any{uuid.NewString(), at(-time.Hour), at(-2 * time.Hour)}},
		{"expired failed key", `INSERT INTO idempotency_keys (idempotency_key, user_id, request_path, request_method, request_hash, status, expires_at, created_at) VALUES (?, 1, '/api/v1/tasks', 'POST', 'h', 'failed', ?, ?)`,
			[]any{uuid.NewString(), at(-time.Hour), at(-2 * time.Hour)}},
		{"expired processing key", `INSERT INTO idempotency_keys (idempotency_key, user_id, request_path, request_method, request_hash, status, expires_at, created_at) VALUES (?, 1, '/api/v1/tasks', 'POST', 'h', 'processing', ?, ?)`,
			[]any{uuid.NewString(), at(-time.Hour), at(-2 * time.Hour)}},
		{"live completed key", `INSERT INTO idempotency_keys (idempotency_key, user_id, request_path, request_method, request_hash, status, expires_at, created_at) VALUES (?, 1, '/api/v1/tasks', 'POST', 'h', 'completed', ?, ?)`,
			[]any{uuid.NewString(), at(time.Hour), at(0)}},
		{"old done outbox", `INSERT INTO outbox_events (user_id, event_type, payload, status, next_attempt_at, published_at, created_at) VALUES (1, 'task.created', '{}', 'done', ?, ?, ?)`,
			[]any{at(-9 * 24 * time.Hour), at(-8 * 24 * time.Hour), at(-9 * 24 * time.Hour)}},
		{"recent done outbox", `INSERT INTO outbox_events (user_id, event_type, payload, status, next_attempt_at, published_at, created_at) VALUES (1, 'task.created', '{}', 'done', ?, ?, ?)`,
			[]any{at(-time.Hour), at(-time.Minute), at(-time.Hour)}},
		{"pending outbox", `INSERT INTO outbox_events (user_id, event_type, payload, status, next_attempt_at, created_at) VALUES (1, 'task.created', '{}', 'pending', ?, ?)`,
			[]any{at(0), at(0)}},
	}
	for _, s := range seed {
		if _, err := database.ExecContext(ctx, s.query, s.args...); err != nil {
			t.Fatalf("seed %s: %v", s.name, err)
		}
	}

	j := janitor.New(database, janitor.Config{SessionRetention: 7 * 24 * time.Hour, OutboxRetention: 7 * 24 * time.Hour})
	report, err := j.RunAll(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Errors) > 0 {
		t.Fatalf("unexpected sweep errors: %v", report.Errors)
	}
	if report.ExpiredSessionsPurged != 2 {
		t.Fatalf("want 2 sessions purged, got %d", report.ExpiredSessionsPurged)
	}
	if report.IdempotencyKeysPurged != 2 {
		t.Fatalf("want 2 idempotency keys purged, got %d", report.IdempotencyKeysPurged)
	}
	if report.OutboxEventsPurged != 1 {
		t.Fatalf("want 1 outbox event purged, got %d", report.OutboxEventsPurged)
	}
	if report.DurationMs < 0 {
		t.Fatalf("report duration is invalid: %d", report.DurationMs)
	}

	counts := map[string]struct {
		query string
		want  int
	}{
		"sessions":         {`SELECT count(*) FROM sessions`, 1},
		"idempotency_keys": {`SELECT count(*) FROM idempotency_keys`, 2},
		"outbox_events":    {`SELECT count(*) FROM outbox_events`, 2},
	}
	for name, check := range counts {
		var got int
		if err := database.QueryRowContext(ctx, check.query).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != check.want {
			t.Fatalf("%s: want %d rows, got %d", name, check.want, got)
		}
	}

	var processing int
	if err := database.QueryRowContext(ctx, `SELECT count(*) FROM idempotency_keys WHERE status = 'processing'`).Scan(&processing); err != nil {
		t.Fatal(err)
	}
	if processing != 1 {
		t.Fatalf("processing keys must survive a sweep, got %d", processing)
	}
}

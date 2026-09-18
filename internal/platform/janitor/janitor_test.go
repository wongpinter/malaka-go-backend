package janitor

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"malaka/internal/platform/testutil"
)

func TestJanitor_RunAll(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping DB-backed test in short mode")
	}
	tdb := testutil.NewTestDB(t)
	defer tdb.Close()
	ctx := context.Background()

	var userID int64
	err := tdb.DB.Pool.QueryRowContext(ctx,
		`INSERT INTO iam.users (email, name, password_hash) VALUES ('janitor@example.com', 'Janitor', 'x') RETURNING id`,
	).Scan(&userID)
	if err != nil {
		t.Fatalf("seed user: %v", err)
	}

	seed := []struct {
		name  string
		query string
		args  []any
	}{
		{"expired session", `INSERT INTO iam.sessions (public_id, user_id, token_hash, expires_at) VALUES ($1, $2, 'hash-expired', now() - interval '1 hour')`, []any{uuid.New(), userID}},
		{"revoked old session", `INSERT INTO iam.sessions (public_id, user_id, token_hash, expires_at, revoked_at) VALUES ($1, $2, 'hash-revoked', now() + interval '1 day', now() - interval '8 days')`, []any{uuid.New(), userID}},
		{"active session", `INSERT INTO iam.sessions (public_id, user_id, token_hash, expires_at) VALUES ($1, $2, 'hash-active', now() + interval '1 day')`, []any{uuid.New(), userID}},
		{"expired completed key", `INSERT INTO public.idempotency_keys (idempotency_key, user_id, request_path, request_method, request_hash, status, expires_at) VALUES ($1, $2, '/api/v1/tasks', 'POST', 'deadbeef', 'completed', now() - interval '1 hour')`, []any{uuid.New(), userID}},
		{"expired failed key", `INSERT INTO public.idempotency_keys (idempotency_key, user_id, request_path, request_method, request_hash, status, expires_at) VALUES ($1, $2, '/api/v1/tasks', 'POST', 'deadbeef', 'failed', now() - interval '1 hour')`, []any{uuid.New(), userID}},
		{"expired processing key", `INSERT INTO public.idempotency_keys (idempotency_key, user_id, request_path, request_method, request_hash, status, expires_at) VALUES ($1, $2, '/api/v1/tasks', 'POST', 'deadbeef', 'processing', now() - interval '1 hour')`, []any{uuid.New(), userID}},
		{"active completed key", `INSERT INTO public.idempotency_keys (idempotency_key, user_id, request_path, request_method, request_hash, status, expires_at) VALUES ($1, $2, '/api/v1/tasks', 'POST', 'deadbeef', 'completed', now() + interval '1 day')`, []any{uuid.New(), userID}},
		{"old done outbox", `INSERT INTO public.outbox_events (event_type, payload, correlation_id, user_id, status, published_at) VALUES ('task.created', '{}'::jsonb, 'cid', $1, 'done', now() - interval '8 days')`, []any{userID}},
		{"recent done outbox", `INSERT INTO public.outbox_events (event_type, payload, correlation_id, user_id, status, published_at) VALUES ('task.created', '{}'::jsonb, 'cid', $1, 'done', now())`, []any{userID}},
		{"pending outbox", `INSERT INTO public.outbox_events (event_type, payload, correlation_id, user_id, status) VALUES ('task.created', '{}'::jsonb, 'cid', $1, 'pending')`, []any{userID}},
	}
	for _, s := range seed {
		if _, err := tdb.DB.Pool.ExecContext(ctx, s.query, s.args...); err != nil {
			t.Fatalf("seed %s: %v", s.name, err)
		}
	}

	j := New(tdb.DB, Config{SessionRetention: 7 * 24 * time.Hour, OutboxRetention: 7 * 24 * time.Hour})
	report, err := j.RunAll(ctx)
	if err != nil {
		t.Fatalf("RunAll: %v", err)
	}
	if len(report.Errors) > 0 {
		t.Fatalf("unexpected sweep errors: %v", report.Errors)
	}

	if report.ExpiredSessionsPurged != 2 {
		t.Errorf("expected 2 sessions purged (expired + old revoked), got %d", report.ExpiredSessionsPurged)
	}
	if report.IdempotencyKeysPurged != 2 {
		t.Errorf("expected 2 idempotency keys purged (completed+failed expired), got %d", report.IdempotencyKeysPurged)
	}
	if report.OutboxEventsPurged != 1 {
		t.Errorf("expected 1 outbox event purged, got %d", report.OutboxEventsPurged)
	}

	assertCount := func(table, where string, want int) {
		t.Helper()
		var n int
		if err := tdb.DB.Pool.QueryRowContext(ctx, "SELECT count(*) FROM "+table+" WHERE "+where).Scan(&n); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if n != want {
			t.Errorf("expected %d rows in %s (%s), got %d", want, table, where, n)
		}
	}
	assertCount("iam.sessions", "token_hash = 'hash-active'", 1)
	assertCount("public.idempotency_keys", "status = 'processing'", 1)
	assertCount("public.idempotency_keys", "expires_at > now()", 1)
	assertCount("public.outbox_events", "true", 2)
}

package mid

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	platformdb "malaka/internal/platform/db"
)

func openClaimStore(t *testing.T) (dbClaimStore, *platformdb.DB) {
	t.Helper()
	database, err := platformdb.OpenSQLite(t.TempDir()+"/malaka.db", platformdb.PoolConfig{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })

	ctx := context.Background()
	if _, err := database.ExecContext(ctx, `
		INSERT INTO users (public_id, email, name, password_hash, created_at, updated_at)
		VALUES (?, 'claimer@example.com', 'Claimer', 'hash', ?, ?)
	`, uuid.NewString(), platformdb.SQLiteTime(time.Now()), platformdb.SQLiteTime(time.Now())); err != nil {
		t.Fatal(err)
	}
	return dbClaimStore{db: database, driver: platformdb.DriverSQLite}, database
}

func claimFor(key, hash string, expires time.Time) claimRequest {
	return claimRequest{
		Scope:     claimScope{Key: key, UserID: 1, Path: "/api/v1/tasks", Method: "POST"},
		Hash:      hash,
		ExpiresAt: expires,
	}
}

func TestDBClaimStore_ForeignKeysAreEnforced(t *testing.T) {
	store, _ := openClaimStore(t)
	_, _, err := store.Claim(context.Background(), claimRequest{
		Scope:     claimScope{Key: uuid.NewString(), UserID: 999, Path: "/api/v1/tasks", Method: "POST"},
		Hash:      "hash",
		ExpiresAt: time.Now().Add(time.Hour),
	})
	if err == nil {
		t.Fatal("expected foreign key violation for unknown user")
	}
}

func TestDBClaimStore_ClaimGetFinalize(t *testing.T) {
	store, _ := openClaimStore(t)
	ctx := context.Background()
	key := uuid.NewString()
	req := claimFor(key, "hash-a", time.Now().Add(time.Hour))

	id, ok, err := store.Claim(ctx, req)
	if err != nil || !ok || id == 0 {
		t.Fatalf("first claim: id=%d ok=%v err=%v", id, ok, err)
	}

	if _, ok, err := store.Claim(ctx, req); err != nil || ok {
		t.Fatalf("second claim must be rejected: ok=%v err=%v", ok, err)
	}

	rec, err := store.Get(ctx, req.Scope)
	if err != nil || rec == nil {
		t.Fatalf("get: rec=%v err=%v", rec, err)
	}
	if rec.Status != "processing" || rec.RequestHash != "hash-a" {
		t.Fatalf("unexpected claim record: %+v", rec)
	}
	if rec.ExpiresAt.IsZero() {
		t.Fatal("expiry was not parsed")
	}

	if err := store.Finalize(ctx, id, claimResult{
		StatusCode:  201,
		Body:        []byte(`{"ok":true}`),
		ContentType: "application/json; charset=utf-8",
		ExpiresAt:   time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}

	rec, err = store.Get(ctx, req.Scope)
	if err != nil || rec == nil {
		t.Fatalf("get after finalize: rec=%v err=%v", rec, err)
	}
	if rec.Status != "completed" || rec.ResponseStatus != 201 || string(rec.ResponseBody) != `{"ok":true}` {
		t.Fatalf("finalize did not persist the response: %+v", rec)
	}
	if rec.ResponseContentType != "application/json; charset=utf-8" {
		t.Fatalf("content type not persisted: %q", rec.ResponseContentType)
	}

	if _, err := store.Get(ctx, claimScope{Key: key, UserID: 1, Path: "/api/v1/tasks", Method: "PUT"}); err != nil {
		t.Fatalf("method scoping must not error: %v", err)
	}
}

func TestDBClaimStore_ConcurrentClaimsElectOneWinner(t *testing.T) {
	store, _ := openClaimStore(t)
	ctx := context.Background()
	req := claimFor(uuid.NewString(), "hash-race", time.Now().Add(time.Hour))

	const goroutines = 20
	var winners, conflicts atomic.Int64
	var wg sync.WaitGroup
	errs := make(chan error, goroutines)
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, ok, err := store.Claim(ctx, req)
			if err != nil {
				errs <- err
				return
			}
			if ok {
				winners.Add(1)
			} else {
				conflicts.Add(1)
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent claim failed: %v", err)
	}
	if winners.Load() != 1 {
		t.Fatalf("want exactly one winner, got %d", winners.Load())
	}
	if conflicts.Load() != goroutines-1 {
		t.Fatalf("want %d conflicts, got %d", goroutines-1, conflicts.Load())
	}

	var rows int
	if err := store.db.(*platformdb.DB).QueryRowContext(ctx, `SELECT count(*) FROM idempotency_keys`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Fatalf("want one stored key, got %d", rows)
	}
}

func TestDBClaimStore_ReclaimsExpiredAndFailedRecords(t *testing.T) {
	store, _ := openClaimStore(t)
	ctx := context.Background()

	expired := claimFor(uuid.NewString(), "hash-old", time.Now().Add(-time.Minute))
	id, ok, err := store.Claim(ctx, expired)
	if err != nil || !ok {
		t.Fatalf("claim: ok=%v err=%v", ok, err)
	}
	if err := store.Finalize(ctx, id, claimResult{StatusCode: 201, Body: []byte("old"), ExpiresAt: time.Now().Add(-time.Minute)}); err != nil {
		t.Fatal(err)
	}

	fresh := claimFor(expired.Scope.Key, "hash-new", time.Now().Add(time.Hour))
	reclaimed, ok, err := store.ReclaimExpired(ctx, id, fresh)
	if err != nil || !ok || reclaimed != id {
		t.Fatalf("reclaim expired: id=%d ok=%v err=%v", reclaimed, ok, err)
	}
	rec, err := store.Get(ctx, fresh.Scope)
	if err != nil || rec == nil {
		t.Fatalf("get: rec=%v err=%v", rec, err)
	}
	if rec.Status != "processing" || rec.RequestHash != "hash-new" || rec.ResponseStatus != 0 || len(rec.ResponseBody) != 0 {
		t.Fatalf("reclaim must reset the record: %+v", rec)
	}

	if err := store.Finalize(ctx, id, claimResult{Failed: true}); err != nil {
		t.Fatal(err)
	}
	rec, err = store.Get(ctx, fresh.Scope)
	if err != nil || rec == nil || rec.Status != "failed" {
		t.Fatalf("failed finalize: rec=%+v err=%v", rec, err)
	}
	if _, ok, err := store.ReclaimFailed(ctx, id, claimFor(fresh.Scope.Key, "different-hash", time.Now().Add(time.Hour))); err != nil || ok {
		t.Fatalf("reclaim with a different hash must fail: ok=%v err=%v", ok, err)
	}
	if _, ok, err := store.ReclaimFailed(ctx, id, fresh); err != nil || !ok {
		t.Fatalf("reclaim failed record: ok=%v err=%v", ok, err)
	}
	if _, ok, err := store.ReclaimFailed(ctx, id, fresh); err != nil || ok {
		t.Fatalf("second reclaim must lose the race: ok=%v err=%v", ok, err)
	}
}

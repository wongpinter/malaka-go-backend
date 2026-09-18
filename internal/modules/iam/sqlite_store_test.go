package iam

import (
	"context"
	"errors"
	"testing"
	"time"

	"malaka/internal/platform/db"
)

func openSQLiteStore(t *testing.T) (*sqliteStore, *db.DB) {
	t.Helper()
	database, err := db.OpenSQLite(t.TempDir()+"/malaka.db", db.PoolConfig{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	return &sqliteStore{db: database}, database
}

func TestSQLiteStoreUserRoundTrip(t *testing.T) {
	store, _ := openSQLiteStore(t)
	ctx := context.Background()

	user, err := store.CreateUser(ctx, "user@example.com", "User", "hash")
	if err != nil {
		t.Fatal(err)
	}
	got, err := store.GetUserByEmail(ctx, "USER@example.com")
	if err != nil || got.ID != user.ID || got.PasswordHash != "hash" {
		t.Fatalf("user round trip: got=%+v err=%v", got, err)
	}
	byID, err := store.GetUserByID(ctx, user.ID)
	if err != nil || byID.PublicID != user.PublicID {
		t.Fatalf("get user by id: got=%+v err=%v", byID, err)
	}
	if _, err := store.GetUserByEmail(ctx, "missing@example.com"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestSQLiteStoreUpsertUserIsIdempotent(t *testing.T) {
	store, _ := openSQLiteStore(t)
	ctx := context.Background()

	first, err := store.UpsertUser(ctx, "seed@example.com", "Seed", "hash-1")
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.UpsertUser(ctx, "seed@example.com", "Seed Renamed", "hash-2")
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != second.ID {
		t.Fatalf("upsert created a second user: %d != %d", first.ID, second.ID)
	}
	if second.Name != "Seed Renamed" || second.PasswordHash != "hash-2" {
		t.Fatalf("upsert did not refresh fields: %+v", second)
	}

	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		go func() {
			_, err := store.UpsertUser(ctx, "race@example.com", "Race", "hash")
			errs <- err
		}()
	}
	for i := 0; i < 8; i++ {
		if err := <-errs; err != nil {
			t.Fatalf("concurrent upsert failed: %v", err)
		}
	}
	var count int
	if err := store.db.QueryRowContext(ctx, `SELECT count(*) FROM users WHERE email = 'race@example.com'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("expected one user row, got %d", count)
	}
}

func TestSQLiteStoreSessionLifetime(t *testing.T) {
	store, _ := openSQLiteStore(t)
	ctx := context.Background()

	user, err := store.CreateUser(ctx, "session@example.com", "Session", "hash")
	if err != nil {
		t.Fatal(err)
	}

	expired, err := store.CreateSession(ctx, user.ID, "expired-hash", time.Now().Add(-time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if expired.PublicID == [16]byte{} {
		t.Fatal("session public id was not populated")
	}
	if _, err := store.GetSessionByTokenHash(ctx, "expired-hash"); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("expired session must be rejected, got %v", err)
	}

	valid, err := store.CreateSession(ctx, user.ID, "valid-hash", time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	got, err := store.GetSessionByTokenHash(ctx, valid.TokenHash)
	if err != nil || got.UserID != user.ID {
		t.Fatalf("valid session lookup failed: got=%+v err=%v", got, err)
	}
	if err := store.RevokeSession(ctx, valid.TokenHash); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetSessionByTokenHash(ctx, valid.TokenHash); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("revoked session must be rejected, got %v", err)
	}
}

func TestSQLiteStoreTimestampsAreFixedWidth(t *testing.T) {
	early := db.SQLiteTime(time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC))
	late := db.SQLiteTime(time.Date(2026, 1, 1, 9, 0, 0, 500_000_000, time.UTC))
	if !(early < late) {
		t.Fatalf("timestamp ordering broken: %q !< %q", early, late)
	}
	parsed, err := db.ParseSQLiteTime(late)
	if err != nil || !parsed.Equal(time.Date(2026, 1, 1, 9, 0, 0, 500_000_000, time.UTC)) {
		t.Fatalf("round trip failed: %v %v", parsed, err)
	}
}

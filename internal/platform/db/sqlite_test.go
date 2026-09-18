package db

import (
	"context"
	"errors"
	"testing"
)

func TestOpenSQLiteSchemaAndRollback(t *testing.T) {
	database, err := OpenSQLite(t.TempDir()+"/malaka.db", PoolConfig{})
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	ctx := context.Background()
	if _, err := database.ExecContext(ctx, `INSERT INTO users (public_id, email, name, password_hash, created_at, updated_at) VALUES ('u-1', 'sqlite@example.com', 'SQLite', 'hash', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}

	err = database.ExecTx(ctx, func(tx Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO tasks (public_id, user_id, title, description, status, created_at, updated_at) VALUES ('task-1', 1, 'rollback', '', 'pending', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`)
		if err != nil {
			return err
		}
		return errors.New("force rollback")
	})
	if err == nil {
		t.Fatal("expected rollback error")
	}

	var count int
	if err := database.QueryRowContext(ctx, `SELECT count(*) FROM tasks`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("rollback failed: got %d tasks", count)
	}
}

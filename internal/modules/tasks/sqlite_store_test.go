package tasks

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
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

func seedUsers(t *testing.T, database *db.DB, extra ...string) {
	t.Helper()
	ctx := context.Background()
	users := append([]string{"owner@example.com", "assignee@example.com"}, extra...)
	for _, email := range users {
		if _, err := database.ExecContext(ctx, `
			INSERT INTO users (public_id, email, name, password_hash, created_at, updated_at)
			VALUES (?, ?, ?, 'hash', ?, ?)
		`, uuid.NewString(), email, email, db.SQLiteTime(time.Now()), db.SQLiteTime(time.Now())); err != nil {
			t.Fatal(err)
		}
	}
}

func createTask(t *testing.T, store *sqliteStore, database *db.DB, userID int64, input Input) *Task {
	t.Helper()
	var created *Task
	if err := database.ExecTx(context.Background(), func(tx db.Tx) error {
		var err error
		created, err = store.Create(context.Background(), tx, userID, input)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return created
}

func TestSQLiteStoreTaskCRUD(t *testing.T) {
	store, database := openSQLiteStore(t)
	seedUsers(t, database)
	ctx := context.Background()

	created := createTask(t, store, database, 1, Input{Title: "Write report", Description: "q3", Status: StatusPending})
	if created.PublicID == uuid.Nil || created.CreatedAt.IsZero() {
		t.Fatalf("create did not populate the task: %+v", created)
	}

	found, err := store.Find(ctx, 1, created.PublicID)
	if err != nil || found.Title != "Write report" {
		t.Fatalf("find: got=%+v err=%v", found, err)
	}
	if _, err := store.Find(ctx, 2, created.PublicID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("other users must not see the task, got %v", err)
	}

	var inTx *Task
	if err := database.ExecTx(ctx, func(tx db.Tx) error {
		var err error
		inTx, err = store.FindTx(ctx, tx, 1, created.PublicID)
		return err
	}); err != nil || inTx.ID != created.ID {
		t.Fatalf("find tx: got=%+v err=%v", inTx, err)
	}

	var updated *Task
	if err := database.ExecTx(ctx, func(tx db.Tx) error {
		var err error
		updated, err = store.Update(ctx, tx, 1, created.PublicID, Input{Title: "Write report v2", Status: StatusInProgress})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if updated.Title != "Write report v2" || updated.Status != StatusInProgress {
		t.Fatalf("update did not apply: %+v", updated)
	}

	if err := database.ExecTx(ctx, func(tx db.Tx) error {
		return store.Delete(ctx, tx, 1, created.PublicID)
	}); err != nil {
		t.Fatal(err)
	}
	if err := database.ExecTx(ctx, func(tx db.Tx) error {
		return store.Delete(ctx, tx, 1, created.PublicID)
	}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second delete must report not found, got %v", err)
	}
}

func TestSQLiteStoreListFilters(t *testing.T) {
	store, database := openSQLiteStore(t)
	seedUsers(t, database)
	ctx := context.Background()

	createTask(t, store, database, 1, Input{Title: "Alpha report", Status: StatusPending})
	createTask(t, store, database, 1, Input{Title: "Beta report", Status: StatusPending})
	createTask(t, store, database, 1, Input{Title: "Gamma note", Status: StatusDone})
	createTask(t, store, database, 1, Input{Title: "Quarterly 100 percent review", Status: StatusDone})
	createTask(t, store, database, 2, Input{Title: "Other user report", Status: StatusPending})

	tests := []struct {
		name   string
		filter ListFilter
		want   int
	}{
		{"no filter", ListFilter{}, 4},
		{"status", ListFilter{Status: StatusDone}, 2},
		{"case insensitive search", ListFilter{Search: "REPORT"}, 2},
		{"search and status", ListFilter{Search: "note", Status: StatusDone}, 1},
		{"literal percent is escaped", ListFilter{Search: "100%"}, 0},
		{"word match after escape", ListFilter{Search: "percent"}, 1},
		{"literal underscore is escaped", ListFilter{Search: "Gamma_note"}, 0},
		{"limit", ListFilter{Limit: 1}, 1},
		{"offset", ListFilter{Offset: 3}, 1},
		{"limit above max falls back to default", ListFilter{Limit: MaxListLimit + 1}, 4},
		{"negative offset clamps", ListFilter{Offset: -5}, 4},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := store.List(ctx, 1, tc.filter)
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != tc.want {
				t.Fatalf("want %d tasks, got %d", tc.want, len(got))
			}
			for _, task := range got {
				if task.UserID != 1 {
					t.Fatalf("list leaked another user's task: %+v", task)
				}
			}
		})
	}
}

func TestSQLiteStoreAssignWritesLogAndChecksAssignee(t *testing.T) {
	store, database := openSQLiteStore(t)
	seedUsers(t, database, "inactive@example.com")
	ctx := context.Background()
	if _, err := database.ExecContext(ctx, `UPDATE users SET is_active = 0 WHERE email = 'inactive@example.com'`); err != nil {
		t.Fatal(err)
	}

	task := createTask(t, store, database, 1, Input{Title: "Assign me", Status: StatusPending})

	err := database.ExecTx(ctx, func(tx db.Tx) error {
		if err := store.AssertActiveUser(ctx, tx, 2); err != nil {
			return err
		}
		assigned, err := store.Assign(ctx, tx, 1, task.PublicID, 2)
		if err != nil {
			return err
		}
		if assigned.AssigneeID == nil || *assigned.AssigneeID != 2 {
			t.Fatalf("assign did not set the assignee: %+v", assigned)
		}
		newAssignee := int64(2)
		return store.InsertLog(ctx, tx, assigned.ID, 1, "assign", nil, &newAssignee, &[]int64{1}[0])
	})
	if err != nil {
		t.Fatal(err)
	}

	var logs int
	if err := database.QueryRowContext(ctx, `SELECT count(*) FROM task_logs WHERE task_id = ? AND action = 'assign'`, task.ID).Scan(&logs); err != nil {
		t.Fatal(err)
	}
	if logs != 1 {
		t.Fatalf("expected one assign log, got %d", logs)
	}

	if err := database.ExecTx(ctx, func(tx db.Tx) error {
		return store.AssertActiveUser(ctx, tx, 3)
	}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("inactive assignee must be rejected, got %v", err)
	}
	if err := database.ExecTx(ctx, func(tx db.Tx) error {
		return store.AssertActiveUser(ctx, tx, 999)
	}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown assignee must be rejected, got %v", err)
	}
	if err := database.ExecTx(ctx, func(tx db.Tx) error {
		_, err := store.Assign(ctx, tx, 2, task.PublicID, 2)
		return err
	}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("assigning another user's task must fail, got %v", err)
	}
}

func TestSQLiteStoreTransactionRollback(t *testing.T) {
	store, database := openSQLiteStore(t)
	seedUsers(t, database)
	ctx := context.Background()

	sentinel := errors.New("boom")
	err := database.ExecTx(ctx, func(tx db.Tx) error {
		if _, err := store.Create(ctx, tx, 1, Input{Title: "Should roll back", Status: StatusPending}); err != nil {
			return err
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("expected sentinel error, got %v", err)
	}

	var count int
	if err := database.QueryRowContext(ctx, `SELECT count(*) FROM tasks`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("rolled back task was persisted: %d rows", count)
	}
}

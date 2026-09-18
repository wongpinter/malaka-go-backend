package testutil_test

import (
	"context"
	"testing"

	"malaka/internal/platform/testutil"
)

func TestPostgresTestcontainer(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	testDB := testutil.NewTestDB(t)

	var generatedUUID string
	err := testDB.DB.Pool.QueryRowContext(context.Background(), "SELECT public.uuid_generate_v7()::text").Scan(&generatedUUID)
	if err != nil {
		t.Fatalf("failed to execute uuid_generate_v7(): %v", err)
	}
	if len(generatedUUID) != 36 {
		t.Fatalf("expected 36 char UUID string, got %s", generatedUUID)
	}

	tables := []string{
		"iam.users",
		"iam.sessions",
		"public.tasks",
		"public.task_logs",
		"public.audit_logs",
		"public.outbox_events",
		"public.idempotency_keys",
	}
	for _, tbl := range tables {
		var count int
		if err := testDB.DB.Pool.QueryRowContext(context.Background(), "SELECT COUNT(*) FROM "+tbl).Scan(&count); err != nil {
			t.Fatalf("failed to query table %s: %v", tbl, err)
		}
	}

	testDB.Truncate(t)

	downBytes, err := testutil.ReadMigrationFile("migrations/000001_init.down.sql")
	if err != nil {
		t.Fatalf("failed to read 000001 down migration: %v", err)
	}
	if _, err := testDB.DB.Pool.ExecContext(context.Background(), string(downBytes)); err != nil {
		t.Fatalf("failed to execute 000001 down migration: %v", err)
	}

	var dummy string
	err = testDB.DB.Pool.QueryRowContext(context.Background(), "SELECT dummy FROM non_existent_table").Scan(&dummy)
	if err == nil {
		t.Fatalf("expected error from non_existent_table, got nil")
	}
}

package audit_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"malaka/internal/platform/audit"
	"malaka/internal/platform/db"
)

func TestWriterSQLite_ChainsAndRedacts(t *testing.T) {
	database, err := db.OpenSQLite(t.TempDir()+"/audit.db", db.PoolConfig{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })

	ctx := context.Background()
	owner := int64(1)
	if _, err := database.ExecContext(ctx, `
		INSERT INTO users (public_id, email, name, password_hash, created_at, updated_at)
		VALUES (?, 'audit@example.com', 'Audit', 'hash', ?, ?)
	`, uuid.NewString(), db.SQLiteTime(time.Now()), db.SQLiteTime(time.Now())); err != nil {
		t.Fatal(err)
	}

	writer := audit.NewWriter(database)
	entityID := uuid.New()
	entries := []audit.AuditEntry{
		{
			UserID:         &owner,
			Action:         "CREATE",
			EntityType:     "task",
			EntityID:       1,
			EntityPublicID: entityID,
			NewValues:      []byte(`{"title":"first","password":"s3cret"}`),
		},
		{
			UserID:         &owner,
			Action:         "UPDATE",
			EntityType:     "task",
			EntityID:       1,
			EntityPublicID: entityID,
			OldValues:      []byte(`{"title":"first"}`),
			NewValues:      []byte(`{"title":"second"}`),
		},
	}
	for _, entry := range entries {
		if err := database.ExecTx(ctx, func(tx db.Tx) error {
			return writer.LogTx(ctx, tx, entry)
		}); err != nil {
			t.Fatal(err)
		}
	}

	rows, err := database.QueryContext(ctx, `SELECT prev_hash, curr_hash, new_values, created_at FROM audit_logs ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()

	type row struct{ prev, curr, newValues, createdAt string }
	var got []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.prev, &r.curr, &r.newValues, &r.createdAt); err != nil {
			t.Fatal(err)
		}
		got = append(got, r)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 audit rows, got %d", len(got))
	}
	if got[0].prev != audit.GenesisHash {
		t.Fatalf("first row must start the chain, got %q", got[0].prev)
	}
	if got[1].prev != got[0].curr {
		t.Fatalf("chain is not linked: %q != %q", got[1].prev, got[0].curr)
	}
	if strings.Contains(got[0].newValues, "s3cret") {
		t.Fatalf("sensitive value was persisted: %s", got[0].newValues)
	}
	if !strings.Contains(got[0].newValues, "REDACTED") {
		t.Fatalf("sensitive field was not redacted: %s", got[0].newValues)
	}

	var first struct{ prev, curr, newValues, action, entityPublicID, createdAt string }
	var entityIDInt int64
	if err := database.QueryRowContext(ctx, `
		SELECT prev_hash, curr_hash, new_values, action, entity_public_id, created_at, entity_id
		FROM audit_logs ORDER BY id LIMIT 1
	`).Scan(&first.prev, &first.curr, &first.newValues, &first.action, &first.entityPublicID, &first.createdAt, &entityIDInt); err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(first.newValues), &decoded); err != nil {
		t.Fatal(err)
	}
	recomputed := audit.ComputeHash(first.prev, first.action, "task", first.entityPublicID, nil, []byte(first.newValues), first.createdAt)
	if recomputed != first.curr {
		t.Fatalf("hash does not match the stored row:\nwant %s\ngot  %s", first.curr, recomputed)
	}
	if entityIDInt != 1 {
		t.Fatalf("entity id was not stored: %d", entityIDInt)
	}
}

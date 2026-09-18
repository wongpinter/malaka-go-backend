package audit_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"malaka/internal/platform/audit"
	"malaka/internal/platform/db"
	"malaka/internal/platform/testutil"
)

func TestWriterPostgres_ChainIsVerifiable(t *testing.T) {
	testDB := testutil.NewTestDB(t)
	ctx := context.Background()

	owner := int64(1)
	email := "audit-pg@example.com"
	if _, err := testDB.DB.ExecContext(ctx, `
		INSERT INTO iam.users (email, name, password_hash) VALUES ($1, 'Audit', 'hash')
	`, email); err != nil {
		t.Fatal(err)
	}
	if err := testDB.DB.QueryRowContext(ctx, `SELECT id FROM iam.users WHERE email = $1`, email).Scan(&owner); err != nil {
		t.Fatal(err)
	}

	writer := audit.NewWriter(testDB.DB)
	entityID := uuid.New()
	for i, newValues := range []string{`{"title":"first"}`, `{"title":"second","password":"s3cret"}`} {
		entry := audit.AuditEntry{
			UserID:         &owner,
			Action:         "UPDATE",
			EntityType:     "task",
			EntityID:       int64(i + 1),
			EntityPublicID: entityID,
			NewValues:      []byte(newValues),
		}
		if err := testDB.DB.ExecTx(ctx, func(tx db.Tx) error {
			return writer.LogTx(ctx, tx, entry)
		}); err != nil {
			t.Fatal(err)
		}
	}

	type row struct {
		prev, curr, action, entityPublicID, newValues string
		createdAt                                     time.Time
	}
	rows, err := testDB.DB.QueryContext(ctx, `
		SELECT prev_hash, curr_hash, action, entity_public_id, new_values, created_at
		FROM public.audit_logs ORDER BY id
	`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()

	var got []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.prev, &r.curr, &r.action, &r.entityPublicID, &r.newValues, &r.createdAt); err != nil {
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
	if strings.Contains(got[1].newValues, "s3cret") {
		t.Fatalf("sensitive value was persisted: %s", got[1].newValues)
	}

	for i, r := range got {
		recomputed := audit.ComputeHash(r.prev, r.action, "task", r.entityPublicID, nil, []byte(r.newValues), db.SQLiteTime(r.createdAt))
		if recomputed != r.curr {
			t.Fatalf("row %d hash does not match the stored data:\nwant %s\ngot  %s", i+1, r.curr, recomputed)
		}
	}
}

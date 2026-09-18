package audit

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/google/uuid"
	platformdb "malaka/internal/platform/db"
	"malaka/pkg/slogx"
)

const GenesisHash = "0000000000000000000000000000000000000000000000000000000000000000"

type AuditEntry struct {
	UserID         *int64
	Action         string
	EntityType     string
	EntityID       int64
	EntityPublicID uuid.UUID
	OldValues      []byte
	NewValues      []byte
}

type Writer struct {
	db platformdb.Executor
}

func NewWriter(executor platformdb.Executor) *Writer {
	return &Writer{db: executor}
}

func ComputeHash(prevHash, action, entityType, entityPublicID string, oldJSON, newJSON []byte, ts string) string {
	h := sha256.New()
	for _, part := range []string{prevHash, action, entityType, entityPublicID, ts} {
		h.Write([]byte(part))
		h.Write([]byte("|"))
	}
	h.Write(oldJSON)
	h.Write([]byte("|"))
	h.Write(newJSON)
	return hex.EncodeToString(h.Sum(nil))
}

func (w *Writer) LogTx(ctx context.Context, tx platformdb.Executor, entry AuditEntry) error {
	var prevHash string
	previousQuery := `
		SELECT curr_hash FROM public.audit_logs
		ORDER BY id DESC LIMIT 1 FOR UPDATE
	`
	if platformdb.DriverOf(tx) == platformdb.DriverSQLite {
		previousQuery = `SELECT curr_hash FROM audit_logs ORDER BY id DESC LIMIT 1`
	}
	err := tx.QueryRowContext(ctx, previousQuery).Scan(&prevHash)
	if err == sql.ErrNoRows {
		prevHash = GenesisHash
	} else if err != nil {
		return fmt.Errorf("fetch prev audit hash: %w", err)
	}

	redactedOld, err := slogx.RedactJSON(entry.OldValues)
	if err != nil {
		return fmt.Errorf("redact old values: %w", err)
	}
	redactedNew, err := slogx.RedactJSON(entry.NewValues)
	if err != nil {
		return fmt.Errorf("redact new values: %w", err)
	}

	now := time.Now().UTC().Truncate(time.Microsecond)
	ts := platformdb.SQLiteTime(now)
	currHash := ComputeHash(prevHash, entry.Action, entry.EntityType, entry.EntityPublicID.String(), redactedOld, redactedNew, ts)

	if platformdb.DriverOf(tx) == platformdb.DriverSQLite {
		_, err = tx.ExecContext(ctx, `
			INSERT INTO audit_logs
				(public_id, user_id, action, entity_type, entity_id, entity_public_id, old_values, new_values, prev_hash, curr_hash, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		`, uuid.New().String(), entry.UserID, entry.Action, entry.EntityType, entry.EntityID, entry.EntityPublicID.String(),
			redactedOld, redactedNew, prevHash, currHash, ts)
	} else {
		_, err = tx.ExecContext(ctx, `
			INSERT INTO public.audit_logs
				(user_id, action, entity_type, entity_id, entity_public_id, old_values, new_values, prev_hash, curr_hash, created_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		`, entry.UserID, entry.Action, entry.EntityType, entry.EntityID, entry.EntityPublicID,
			redactedOld, redactedNew, prevHash, currHash, now)
	}
	if err != nil {
		return fmt.Errorf("insert audit log: %w", err)
	}
	return nil
}

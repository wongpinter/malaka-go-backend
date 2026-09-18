package event

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	platformdb "malaka/internal/platform/db"
	"malaka/internal/platform/mid"
	"malaka/pkg/id"
	"malaka/pkg/slogx"
)

const maxOutboxAttempts = 10

func Add(ctx context.Context, tx platformdb.Executor, eventType string, payload any) error {
	b, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal outbox payload: %w", err)
	}
	if redacted, rerr := slogx.RedactJSON(b); rerr == nil {
		b = redacted
	}
	cid := mid.GetCorrelationID(ctx)
	if cid == "" {
		cid = id.New().String()
	}
	var userID *int64
	if uid := mid.GetUserID(ctx); uid != 0 {
		userID = &uid
	}
	query := `
		INSERT INTO public.outbox_events (event_type, payload, correlation_id, user_id)
		VALUES ($1, $2, $3, $4)
	`
	args := []any{eventType, b, cid, userID}
	if platformdb.DriverOf(tx) == platformdb.DriverSQLite {
		query = `INSERT INTO outbox_events (event_type, payload, correlation_id, user_id, next_attempt_at, created_at) VALUES (?, ?, ?, ?, ?, ?)`
		now := platformdb.SQLiteTime(time.Now())
		args = []any{eventType, b, cid, userID, now, now}
	}
	_, err = tx.ExecContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("insert outbox event: %w", err)
	}
	return nil
}

type OutboxRelay struct {
	db        platformdb.Database
	pool      *sql.DB
	bus       *Bus
	interval  time.Duration
	batchSize int
}

func NewOutboxRelay(database platformdb.Database, bus *Bus, interval time.Duration) *OutboxRelay {
	if interval <= 0 {
		interval = 500 * time.Millisecond
	}
	return &OutboxRelay{db: database, pool: database.Raw(), bus: bus, interval: interval, batchSize: 50}
}

func (r *OutboxRelay) Run(ctx context.Context) error {
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := r.processBatch(ctx); err != nil {
				slog.ErrorContext(ctx, "outbox poller batch error", "error_type", fmt.Sprintf("%T", err))
			}
		}
	}
}

func (r *OutboxRelay) processBatch(ctx context.Context) error {
	if r.db.Driver() == platformdb.DriverSQLite {
		return r.processBatchSQLite(ctx)
	}
	return r.processBatchPostgres(ctx)
}

func (r *OutboxRelay) processBatchSQLite(ctx context.Context) error {
	type item struct {
		id            int64
		typ           string
		payload       []byte
		attempts      int
		correlationID sql.NullString
		userID        sql.NullInt64
	}
	var items []item
	err := r.db.ExecTx(ctx, func(tx platformdb.Tx) error {
		rows, err := tx.QueryContext(ctx, `
			SELECT id, event_type, payload, attempts, correlation_id, user_id
			FROM outbox_events
			WHERE status IN ('pending', 'failed', 'processing') AND next_attempt_at <= ?
			ORDER BY created_at ASC LIMIT ?
		`, platformdb.SQLiteTime(time.Now()), r.batchSize)
		if err != nil {
			return fmt.Errorf("query pending sqlite outbox: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var it item
			if err := rows.Scan(&it.id, &it.typ, &it.payload, &it.attempts, &it.correlationID, &it.userID); err != nil {
				return fmt.Errorf("scan sqlite outbox row: %w", err)
			}
			items = append(items, it)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		nextAttempt := platformdb.SQLiteTime(time.Now().Add(5 * time.Minute))
		for _, it := range items {
			if _, err := tx.ExecContext(ctx, `UPDATE outbox_events SET status = 'processing', attempts = attempts + 1, next_attempt_at = ? WHERE id = ?`, nextAttempt, it.id); err != nil {
				return fmt.Errorf("mark sqlite outbox processing: %w", err)
			}
		}
		return nil
	})
	if err != nil || len(items) == 0 {
		return err
	}

	for _, it := range items {
		ev := Event{Type: it.typ, Payload: json.RawMessage(it.payload)}
		if it.correlationID.Valid {
			ev.CorrelationID = it.correlationID.String
		}
		if it.userID.Valid {
			ev.UserID = it.userID.Int64
		}
		publishCtx := ctx
		if ev.CorrelationID != "" {
			publishCtx = mid.WithCorrelationID(publishCtx, ev.CorrelationID)
		}
		if ev.UserID != 0 {
			publishCtx = mid.WithUserID(publishCtx, ev.UserID)
		}
		deliveryErr := r.bus.PublishAndWait(publishCtx, ev)
		updateCtx, updateCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		if deliveryErr != nil {
			if it.attempts+1 >= maxOutboxAttempts {
				if _, err := r.db.ExecContext(updateCtx, `UPDATE outbox_events SET status = 'dead', next_attempt_at = NULL WHERE id = ?`, it.id); err != nil {
					slog.ErrorContext(updateCtx, "failed to dead-letter sqlite outbox event", "id", it.id, "error_type", fmt.Sprintf("%T", err))
				}
			} else {
				next := platformdb.SQLiteTime(time.Now().Add(outboxBackoff(it.attempts + 1)))
				if _, err := r.db.ExecContext(updateCtx, `UPDATE outbox_events SET status = 'failed', next_attempt_at = ? WHERE id = ?`, next, it.id); err != nil {
					slog.ErrorContext(updateCtx, "failed to mark sqlite outbox for retry", "id", it.id, "error_type", fmt.Sprintf("%T", err))
				}
			}
		} else if _, err := r.db.ExecContext(updateCtx, `UPDATE outbox_events SET status = 'done', published_at = ? WHERE id = ?`, platformdb.SQLiteTime(time.Now()), it.id); err != nil {
			slog.ErrorContext(updateCtx, "failed to mark sqlite outbox done", "id", it.id, "error_type", fmt.Sprintf("%T", err))
		}
		updateCancel()
	}
	return nil
}

func (r *OutboxRelay) processBatchPostgres(ctx context.Context) error {
	tx, err := r.pool.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	rows, err := tx.QueryContext(ctx, `
		SELECT id, event_type, payload, attempts, correlation_id, user_id
		FROM public.outbox_events
		WHERE status IN ('pending', 'failed', 'processing')
		  AND next_attempt_at <= now()
		ORDER BY created_at ASC
		LIMIT $1
		FOR UPDATE SKIP LOCKED
	`, r.batchSize)
	if err != nil {
		return fmt.Errorf("query pending outbox: %w", err)
	}
	defer rows.Close()

	type item struct {
		id            int64
		typ           string
		payload       json.RawMessage
		attempts      int
		correlationID sql.NullString
		userID        sql.NullInt64
	}

	var items []item
	for rows.Next() {
		var it item
		if err := rows.Scan(&it.id, &it.typ, &it.payload, &it.attempts, &it.correlationID, &it.userID); err != nil {
			return fmt.Errorf("scan outbox row: %w", err)
		}
		items = append(items, it)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	rows.Close()

	if len(items) == 0 {
		return tx.Commit()
	}

	for _, it := range items {
		if _, err := tx.ExecContext(ctx, `UPDATE public.outbox_events SET status = 'processing', attempts = attempts + 1, next_attempt_at = now() + interval '5 minutes' WHERE id = $1`, it.id); err != nil {
			return fmt.Errorf("mark outbox processing: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit outbox batch: %w", err)
	}

	for _, it := range items {
		ev := Event{Type: it.typ, Payload: it.payload}
		if it.correlationID.Valid {
			ev.CorrelationID = it.correlationID.String
		}
		if it.userID.Valid {
			ev.UserID = it.userID.Int64
		}

		publishCtx := ctx
		if ev.CorrelationID != "" {
			publishCtx = mid.WithCorrelationID(publishCtx, ev.CorrelationID)
		}
		if ev.UserID != 0 {
			publishCtx = mid.WithUserID(publishCtx, ev.UserID)
		}

		deliveryErr := r.bus.PublishAndWait(publishCtx, ev)

		updateCtx, updateCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		if deliveryErr != nil {
			if it.attempts+1 >= maxOutboxAttempts {
				if _, err := r.pool.ExecContext(updateCtx, `UPDATE public.outbox_events SET status = 'dead', next_attempt_at = NULL WHERE id = $1`, it.id); err != nil {
					slog.ErrorContext(updateCtx, "failed to dead-letter outbox event", "id", it.id, "error_type", fmt.Sprintf("%T", err))
				}
				updateCancel()
				continue
			}
			nextAttempt := time.Now().Add(outboxBackoff(it.attempts + 1))
			if _, err := r.pool.ExecContext(updateCtx, `UPDATE public.outbox_events SET status = 'failed', next_attempt_at = $2 WHERE id = $1`, it.id, nextAttempt); err != nil {
				slog.ErrorContext(updateCtx, "failed to mark outbox for retry", "id", it.id, "error_type", fmt.Sprintf("%T", err))
			}
		} else {
			if _, err := r.pool.ExecContext(updateCtx, `UPDATE public.outbox_events SET status = 'done', published_at = now() WHERE id = $1`, it.id); err != nil {
				slog.ErrorContext(updateCtx, "failed to mark outbox done", "id", it.id, "error_type", fmt.Sprintf("%T", err))
			}
		}
		updateCancel()
	}
	return nil
}

func outboxBackoff(attempts int) time.Duration {
	backoff := 5 * time.Second
	for i := 1; i < attempts && backoff < 15*time.Minute; i++ {
		backoff *= 2
	}
	if backoff > 15*time.Minute {
		return 15 * time.Minute
	}
	return backoff
}

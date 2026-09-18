package janitor

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"time"

	platformdb "malaka/internal/platform/db"
)

type Config struct {
	SessionRetention time.Duration
	OutboxRetention  time.Duration
}

func DefaultConfig() Config {
	return Config{
		SessionRetention: 7 * 24 * time.Hour,
		OutboxRetention:  7 * 24 * time.Hour,
	}
}

type Report struct {
	StartedAt             time.Time         `json:"started_at"`
	FinishedAt            time.Time         `json:"finished_at"`
	DurationMs            int64             `json:"duration_ms"`
	ExpiredSessionsPurged int64             `json:"expired_sessions_purged"`
	OutboxEventsPurged    int64             `json:"outbox_events_purged"`
	IdempotencyKeysPurged int64             `json:"idempotency_keys_purged"`
	Errors                map[string]string `json:"errors,omitempty"`
}

type Janitor struct {
	db  platformdb.Database
	cfg Config
}

func New(database platformdb.Database, cfg ...Config) *Janitor {
	c := DefaultConfig()
	if len(cfg) > 0 {
		if cfg[0].SessionRetention > 0 {
			c.SessionRetention = cfg[0].SessionRetention
		}
		if cfg[0].OutboxRetention > 0 {
			c.OutboxRetention = cfg[0].OutboxRetention
		}
	}
	return &Janitor{db: database, cfg: c}
}

func (j *Janitor) PurgeExpiredSessions(ctx context.Context, olderThan time.Duration) (int64, error) {
	var (
		res sql.Result
		err error
	)
	if j.db.Driver() == platformdb.DriverSQLite {
		now := time.Now().UTC()
		res, err = j.db.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at < ? OR (revoked_at IS NOT NULL AND revoked_at < ?)`, platformdb.SQLiteTime(now), platformdb.SQLiteTime(now.Add(-olderThan)))
	} else {
		cutoff := fmt.Sprintf("%d seconds", int(olderThan.Seconds()))
		res, err = j.db.ExecContext(ctx, `DELETE FROM iam.sessions WHERE expires_at < now() OR (revoked_at IS NOT NULL AND revoked_at < now() - $1::interval)`, cutoff)
	}
	if err != nil {
		return 0, fmt.Errorf("purge expired sessions: %w", err)
	}
	return res.RowsAffected()
}

func (j *Janitor) PurgeProcessedOutboxEvents(ctx context.Context, olderThan time.Duration) (int64, error) {
	var (
		res sql.Result
		err error
	)
	if j.db.Driver() == platformdb.DriverSQLite {
		res, err = j.db.ExecContext(ctx, `DELETE FROM outbox_events WHERE status = 'done' AND published_at < ?`, platformdb.SQLiteTime(time.Now().Add(-olderThan)))
	} else {
		cutoff := fmt.Sprintf("%d seconds", int(olderThan.Seconds()))
		res, err = j.db.ExecContext(ctx, `DELETE FROM public.outbox_events WHERE status = 'done' AND published_at < now() - $1::interval`, cutoff)
	}
	if err != nil {
		return 0, fmt.Errorf("purge processed outbox events: %w", err)
	}
	return res.RowsAffected()
}

func (j *Janitor) PurgeExpiredIdempotencyKeys(ctx context.Context) (int64, error) {
	var (
		res sql.Result
		err error
	)
	if j.db.Driver() == platformdb.DriverSQLite {
		res, err = j.db.ExecContext(ctx, `DELETE FROM idempotency_keys WHERE expires_at <= ? AND status IN ('completed', 'failed')`, platformdb.SQLiteTime(time.Now()))
	} else {
		res, err = j.db.ExecContext(ctx, `DELETE FROM public.idempotency_keys WHERE expires_at <= now() AND status IN ('completed', 'failed')`)
	}
	if err != nil {
		return 0, fmt.Errorf("purge expired idempotency keys: %w", err)
	}
	return res.RowsAffected()
}

func (j *Janitor) RunAll(ctx context.Context) (*Report, error) {
	start := time.Now()
	report := &Report{StartedAt: start, Errors: make(map[string]string)}

	if n, err := j.PurgeExpiredSessions(ctx, j.cfg.SessionRetention); err != nil {
		report.Errors["sessions"] = fmt.Sprintf("%T", err)
	} else {
		report.ExpiredSessionsPurged = n
	}

	if n, err := j.PurgeProcessedOutboxEvents(ctx, j.cfg.OutboxRetention); err != nil {
		report.Errors["outbox_events"] = fmt.Sprintf("%T", err)
	} else {
		report.OutboxEventsPurged = n
	}

	if n, err := j.PurgeExpiredIdempotencyKeys(ctx); err != nil {
		report.Errors["idempotency_keys"] = fmt.Sprintf("%T", err)
	} else {
		report.IdempotencyKeysPurged = n
	}

	finish := time.Now()
	report.FinishedAt = finish
	report.DurationMs = finish.Sub(start).Milliseconds()

	slog.InfoContext(ctx, "janitor sweep completed",
		"duration_ms", report.DurationMs,
		"sessions_purged", report.ExpiredSessionsPurged,
		"outbox_purged", report.OutboxEventsPurged,
		"idempotency_purged", report.IdempotencyKeysPurged,
		"error_count", len(report.Errors),
	)
	return report, nil
}

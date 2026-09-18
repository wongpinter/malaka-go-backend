package dblog

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

type Logger struct {
	slowThreshold time.Duration
}

func New(slowThreshold time.Duration) *Logger {
	if slowThreshold <= 0 {
		slowThreshold = 200 * time.Millisecond
	}
	return &Logger{slowThreshold: slowThreshold}
}

func (l *Logger) Log(ctx context.Context, op string, duration time.Duration, err error) {
	if err != nil {
		attrs := []any{
			"op", op,
			"duration_ms", duration.Milliseconds(),
			"error_type", fmt.Sprintf("%T", err),
		}
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) {
			attrs = append(attrs,
				"pg_code", pgErr.Code,
				"pg_severity", pgErr.Severity,
			)
			if pgErr.TableName != "" {
				attrs = append(attrs, "pg_table", pgErr.TableName)
			}
			if pgErr.ConstraintName != "" {
				attrs = append(attrs, "pg_constraint", pgErr.ConstraintName)
			}
		}
		slog.ErrorContext(ctx, "db query error", attrs...)
		return
	}

	if duration >= l.slowThreshold {
		slog.WarnContext(ctx, "slow db query detected",
			"op", op,
			"duration_ms", duration.Milliseconds(),
			"threshold_ms", l.slowThreshold.Milliseconds(),
		)
		return
	}

	slog.DebugContext(ctx, "db query executed",
		"op", op,
		"duration_ms", duration.Milliseconds(),
	)
}

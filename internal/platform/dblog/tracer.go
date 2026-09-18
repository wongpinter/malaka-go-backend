package dblog

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"runtime"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type traceQueryKey struct{}

type queryTraceData struct {
	sql       string
	startTime time.Time
}

type QueryTracer struct {
	slowThreshold time.Duration
	logAllQueries bool
}

func NewQueryTracer(slowThreshold time.Duration) *QueryTracer {
	if slowThreshold <= 0 {
		slowThreshold = 200 * time.Millisecond
	}
	return &QueryTracer{
		slowThreshold: slowThreshold,
		logAllQueries: false,
	}
}

func (t *QueryTracer) SetLogAllQueries(enabled bool) {
	t.logAllQueries = enabled
}

func (t *QueryTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	return context.WithValue(ctx, traceQueryKey{}, &queryTraceData{
		sql:       data.SQL,
		startTime: time.Now(),
	})
}

func (t *QueryTracer) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryEndData) {
	traceData, ok := ctx.Value(traceQueryKey{}).(*queryTraceData)
	if !ok || traceData == nil {
		return
	}

	duration := time.Since(traceData.startTime)
	err := data.Err

	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			cleanSQL := strings.TrimSpace(traceData.sql)
			attrs := []slog.Attr{
				slog.String("query", cleanSQL),
				slog.Int64("duration_ms", duration.Milliseconds()),
				slog.String("error_type", fmt.Sprintf("%T", err)),
			}

			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) {
				attrs = append(attrs,
					slog.String("pg_code", pgErr.Code),
					slog.String("pg_severity", pgErr.Severity),
				)
				if pgErr.ConstraintName != "" {
					attrs = append(attrs, slog.String("pg_constraint", pgErr.ConstraintName))
				}
				if pgErr.TableName != "" {
					attrs = append(attrs, slog.String("pg_table", pgErr.TableName))
				}
			}

			if caller := findAppCaller(); caller != "" {
				attrs = append(attrs, slog.String("caller", caller))
			}

			slog.LogAttrs(ctx, slog.LevelError, "database query execution failed", attrs...)
		}
		return
	}

	if duration >= t.slowThreshold {
		cleanSQL := strings.TrimSpace(traceData.sql)
		attrs := []slog.Attr{
			slog.String("query", cleanSQL),
			slog.Int64("duration_ms", duration.Milliseconds()),
			slog.Int64("threshold_ms", t.slowThreshold.Milliseconds()),
		}
		if caller := findAppCaller(); caller != "" {
			attrs = append(attrs, slog.String("caller", caller))
		}
		slog.LogAttrs(ctx, slog.LevelWarn, "slow database query detected", attrs...)
		return
	}

	if t.logAllQueries || slog.Default().Enabled(ctx, slog.LevelDebug) {
		cleanSQL := strings.TrimSpace(traceData.sql)
		attrs := []slog.Attr{
			slog.String("query", cleanSQL),
			slog.Int64("duration_ms", duration.Milliseconds()),
		}

		if data.CommandTag.RowsAffected() > 0 {
			attrs = append(attrs, slog.Int64("rows_affected", data.CommandTag.RowsAffected()))
		}

		if caller := findAppCaller(); caller != "" {
			attrs = append(attrs, slog.String("caller", caller))
		}

		slog.LogAttrs(ctx, slog.LevelDebug, "database query executed", attrs...)
	}
}

func findAppCaller() string {
	const maxDepth = 25
	for i := 3; i < maxDepth; i++ {
		pc, file, line, ok := runtime.Caller(i)
		if !ok {
			break
		}

		if strings.Contains(file, "runtime/") ||
			strings.Contains(file, "database/sql") ||
			strings.Contains(file, "jackc/pgx") ||
			strings.Contains(file, "internal/platform/dblog") ||
			strings.Contains(file, "internal/platform/db/") {
			continue
		}

		fn := runtime.FuncForPC(pc)
		fnName := ""
		if fn != nil {
			parts := strings.Split(fn.Name(), ".")
			fnName = parts[len(parts)-1]
		}

		shortFile := file
		if idx := strings.Index(file, "malaka/"); idx != -1 {
			shortFile = file[idx:]
		}

		if fnName != "" {
			return fmt.Sprintf("%s:%d (%s)", shortFile, line, fnName)
		}
		return fmt.Sprintf("%s:%d", shortFile, line)
	}
	return ""
}

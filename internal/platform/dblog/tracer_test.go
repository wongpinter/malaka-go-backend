package dblog_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"malaka/internal/platform/dblog"
)

func TestQueryTracer_ErrorLogging(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}))
	slog.SetDefault(logger)

	tracer := dblog.NewQueryTracer(100 * time.Millisecond)

	ctx := context.Background()
	query := "SELECT * FROM non_existent_table WHERE id = $1 AND password = $2"
	args := []any{42, "supersecret"}

	ctx = tracer.TraceQueryStart(ctx, nil, pgx.TraceQueryStartData{
		SQL:  query,
		Args: args,
	})

	pgErr := &pgconn.PgError{
		Severity:       "ERROR",
		Code:           "42P01",
		Message:        `relation "non_existent_table" does not exist`,
		TableName:      "non_existent_table",
		ConstraintName: "",
	}

	tracer.TraceQueryEnd(ctx, nil, pgx.TraceQueryEndData{
		Err: pgErr,
	})

	var logged map[string]any
	if err := json.Unmarshal(buf.Bytes(), &logged); err != nil {
		t.Fatalf("failed to parse slog output: %v\nRaw: %s", err, buf.String())
	}

	if logged["level"] != "ERROR" {
		t.Errorf("expected level ERROR, got %v", logged["level"])
	}

	if logged["msg"] != "database query execution failed" {
		t.Errorf("expected message 'database query execution failed', got %v", logged["msg"])
	}

	if logged["query"] != query {
		t.Errorf("expected query %q, got %q", query, logged["query"])
	}

	if logged["pg_code"] != "42P01" {
		t.Errorf("expected pg_code '42P01', got %v", logged["pg_code"])
	}

	if logged["pg_table"] != "non_existent_table" {
		t.Errorf("expected pg_table 'non_existent_table', got %v", logged["pg_table"])
	}
	if _, ok := logged["args"]; ok {
		t.Error("query arguments must not be logged")
	}
	if _, ok := logged["error"]; ok {
		t.Error("raw database error must not be logged")
	}

	if logged["caller"] == nil || logged["caller"] == "" {
		t.Errorf("expected caller location to be logged")
	}
}

func TestQueryTracer_SkipErrNoRows(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}))
	slog.SetDefault(logger)

	tracer := dblog.NewQueryTracer(100 * time.Millisecond)

	ctx := context.Background()
	ctx = tracer.TraceQueryStart(ctx, nil, pgx.TraceQueryStartData{
		SQL: "SELECT id FROM incoming_letters WHERE id = $1",
	})

	tracer.TraceQueryEnd(ctx, nil, pgx.TraceQueryEndData{
		Err: sql.ErrNoRows,
	})

	if buf.Len() != 0 {
		t.Errorf("expected sql.ErrNoRows not to log an error, got: %s", buf.String())
	}
}

func TestQueryTracer_SlowQueryWarning(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}))
	slog.SetDefault(logger)

	tracer := dblog.NewQueryTracer(5 * time.Millisecond)

	ctx := context.Background()
	ctx = tracer.TraceQueryStart(ctx, nil, pgx.TraceQueryStartData{
		SQL: "SELECT pg_sleep(0.01)",
	})

	time.Sleep(10 * time.Millisecond)

	tracer.TraceQueryEnd(ctx, nil, pgx.TraceQueryEndData{
		Err: nil,
	})

	var logged map[string]any
	if err := json.Unmarshal(buf.Bytes(), &logged); err != nil {
		t.Fatalf("failed to parse slog output: %v", err)
	}

	if logged["level"] != "WARN" {
		t.Errorf("expected level WARN for slow query, got %v", logged["level"])
	}

	if logged["msg"] != "slow database query detected" {
		t.Errorf("expected msg 'slow database query detected', got %v", logged["msg"])
	}
}

func TestQueryTracer_StandardError(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}))
	slog.SetDefault(logger)

	tracer := dblog.NewQueryTracer(100 * time.Millisecond)

	ctx := context.Background()
	ctx = tracer.TraceQueryStart(ctx, nil, pgx.TraceQueryStartData{
		SQL: "INSERT INTO test VALUES (1)",
	})

	tracer.TraceQueryEnd(ctx, nil, pgx.TraceQueryEndData{
		Err: errors.New("connection reset by peer"),
	})

	var logged map[string]any
	if err := json.Unmarshal(buf.Bytes(), &logged); err != nil {
		t.Fatalf("failed to parse slog output: %v", err)
	}

	if logged["level"] != "ERROR" {
		t.Errorf("expected level ERROR, got %v", logged["level"])
	}

	if _, ok := logged["error"]; ok {
		t.Error("raw database error must not be logged")
	}
	if logged["error_type"] != "*errors.errorString" {
		t.Errorf("expected error type, got %v", logged["error_type"])
	}
}

func TestQueryTracer_NormalQueryInDebugMode(t *testing.T) {
	var debugBuf bytes.Buffer
	debugLogger := slog.New(slog.NewJSONHandler(&debugBuf, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}))
	slog.SetDefault(debugLogger)

	tracer := dblog.NewQueryTracer(500 * time.Millisecond)

	ctx := context.Background()
	ctx = tracer.TraceQueryStart(ctx, nil, pgx.TraceQueryStartData{
		SQL:  "SELECT id, letter_number FROM incoming_letters WHERE organization_id = $1",
		Args: []any{10},
	})

	tracer.TraceQueryEnd(ctx, nil, pgx.TraceQueryEndData{
		CommandTag: pgconn.NewCommandTag("SELECT 5"),
		Err:        nil,
	})

	var logged map[string]any
	if err := json.Unmarshal(debugBuf.Bytes(), &logged); err != nil {
		t.Fatalf("failed to decode debug log: %v", err)
	}

	if logged["level"] != "DEBUG" {
		t.Errorf("expected level DEBUG, got %v", logged["level"])
	}
	if logged["msg"] != "database query executed" {
		t.Errorf("expected msg 'database query executed', got %v", logged["msg"])
	}
	if logged["query"] != "SELECT id, letter_number FROM incoming_letters WHERE organization_id = $1" {
		t.Errorf("unexpected query logged: %v", logged["query"])
	}
	if logged["rows_affected"] != float64(5) {
		t.Errorf("expected rows_affected 5, got %v", logged["rows_affected"])
	}

	var infoBuf bytes.Buffer
	infoLogger := slog.New(slog.NewJSONHandler(&infoBuf, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	slog.SetDefault(infoLogger)

	ctx2 := context.Background()
	ctx2 = tracer.TraceQueryStart(ctx2, nil, pgx.TraceQueryStartData{
		SQL:  "SELECT 1",
		Args: nil,
	})
	tracer.TraceQueryEnd(ctx2, nil, pgx.TraceQueryEndData{
		Err: nil,
	})

	if infoBuf.Len() != 0 {
		t.Errorf("expected normal query to be silenced when level is INFO, got: %s", infoBuf.String())
	}
}

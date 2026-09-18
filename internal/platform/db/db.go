package db

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"malaka/internal/platform/dblog"
)

type DB struct {
	Pool   *sql.DB
	logger *dblog.Logger
	tracer *dblog.QueryTracer
	driver Driver
}

type PoolConfig struct {
	MaxOpenConns    int
	MaxIdleConns    int
	ConnMaxLifetime time.Duration
	SlowThreshold   time.Duration
}

func New(dsn string, cfg PoolConfig) (*DB, error) {
	connConfig, err := pgx.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("failed to parse pgx dsn: %w", err)
	}

	tracer := dblog.NewQueryTracer(cfg.SlowThreshold)
	connConfig.Tracer = tracer

	pool := stdlib.OpenDB(*connConfig)

	maxOpen := cfg.MaxOpenConns
	if maxOpen <= 0 {
		maxOpen = 25
	}
	maxIdle := cfg.MaxIdleConns
	if maxIdle <= 0 {
		maxIdle = 5
	}
	connLifetime := cfg.ConnMaxLifetime
	if connLifetime <= 0 {
		connLifetime = 5 * time.Minute
	}

	pool.SetMaxOpenConns(maxOpen)
	pool.SetMaxIdleConns(maxIdle)
	pool.SetConnMaxLifetime(connLifetime)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := pool.PingContext(ctx); err != nil {
		_ = pool.Close()
		return nil, fmt.Errorf("failed to ping database: %w", err)
	}

	logger := dblog.New(cfg.SlowThreshold)
	return &DB{Pool: pool, logger: logger, tracer: tracer, driver: DriverPostgres}, nil
}

func (d *DB) Close() {
	slog.Debug("closing database connection pool")
	_ = d.Pool.Close()
}

func (d *DB) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return d.Pool.ExecContext(ctx, query, args...)
}

func (d *DB) PrepareContext(ctx context.Context, query string) (*sql.Stmt, error) {
	return d.Pool.PrepareContext(ctx, query)
}

func (d *DB) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return d.Pool.QueryContext(ctx, query, args...)
}

func (d *DB) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return d.Pool.QueryRowContext(ctx, query, args...)
}

func (d *DB) Ping(ctx context.Context) error {
	return d.Pool.PingContext(ctx)
}

func (d *DB) Driver() Driver { return d.driver }

func (d *DB) Raw() *sql.DB { return d.Pool }

func (d *DB) ExecTx(ctx context.Context, fn func(Tx) error) error {
	tx, err := d.Pool.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin transaction failed: %w", err)
	}

	defer func() {
		if p := recover(); p != nil {
			_ = tx.Rollback()
			panic(p)
		}
	}()

	start := time.Now()
	err = fn(txAdapter{Tx: tx, driver: d.driver})
	duration := time.Since(start)

	if err != nil {
		_ = tx.Rollback()
		d.logger.Log(ctx, "transaction rollback", duration, err)
		return err
	}

	if err := tx.Commit(); err != nil {
		d.logger.Log(ctx, "transaction commit error", duration, err)
		return fmt.Errorf("commit transaction failed: %w", err)
	}

	d.logger.Log(ctx, "transaction committed", duration, nil)
	return nil
}

type txAdapter struct {
	*sql.Tx
	driver Driver
}

func (t txAdapter) Driver() Driver { return t.driver }

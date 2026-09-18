package db

import (
	"context"
	"database/sql"
	_ "embed"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"malaka/internal/platform/dblog"
	_ "modernc.org/sqlite"
)

//go:embed sqlite_schema.sql
var sqliteSchema string

func Open(driver Driver, dsn string, cfg PoolConfig) (*DB, error) {
	switch driver {
	case DriverSQLite:
		return OpenSQLite(dsn, cfg)
	case DriverPostgres:
		return New(dsn, cfg)
	default:
		return nil, fmt.Errorf("unsupported database driver %q", driver)
	}
}

func OpenSQLite(path string, cfg PoolConfig) (*DB, error) {
	pool, err := openSQLitePool(path, cfg)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if _, err := pool.ExecContext(ctx, sqliteSchema); err != nil {
		_ = pool.Close()
		return nil, fmt.Errorf("migrate sqlite: %w", err)
	}
	if err := pool.PingContext(ctx); err != nil {
		_ = pool.Close()
		return nil, fmt.Errorf("ping sqlite: %w", err)
	}

	return &DB{Pool: pool, logger: dblog.New(cfg.SlowThreshold), driver: DriverSQLite}, nil
}

func sqliteDSN(path string) (string, error) {
	q := url.Values{}
	for _, p := range []string{"foreign_keys(1)", "journal_mode(WAL)", "busy_timeout(5000)"} {
		q.Add("_pragma", p)
	}
	q.Set("_txlock", "immediate")

	if path == ":memory:" {
		return ":memory:?" + q.Encode(), nil
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	u := url.URL{Scheme: "file", Path: abs, RawQuery: q.Encode()}
	return u.String(), nil
}

func openSQLitePool(path string, cfg PoolConfig) (*sql.DB, error) {
	if path != ":memory:" {
		if dir := filepath.Dir(path); dir != "." {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return nil, fmt.Errorf("create sqlite directory: %w", err)
			}
		}
	}

	dsn, err := sqliteDSN(path)
	if err != nil {
		return nil, fmt.Errorf("resolve sqlite path: %w", err)
	}
	pool, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	pool.SetMaxOpenConns(1)
	pool.SetMaxIdleConns(1)
	if cfg.ConnMaxLifetime > 0 {
		pool.SetConnMaxLifetime(cfg.ConnMaxLifetime)
	}
	return pool, nil
}

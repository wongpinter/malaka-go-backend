package testutil

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"malaka/internal/platform/db"
	"malaka/internal/platform/mid"
)

func AuthMiddleware(userID int64) mid.Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := mid.WithUserID(r.Context(), userID)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

type TestDatabase struct {
	DB        *db.DB
	DSN       string
	container *tcpostgres.PostgresContainer
}

func NewTestDB(t testing.TB) *TestDatabase {
	t.Helper()
	ctx := context.Background()

	var (
		dsn       string
		container *tcpostgres.PostgresContainer
	)

	envDSN := os.Getenv("TEST_DATABASE_URL")
	if envDSN != "" {
		dsn = envDSN
	} else {
		pgC, err := tcpostgres.Run(ctx,
			"postgres:16-alpine",
			tcpostgres.WithDatabase("malaka_test"),
			tcpostgres.WithUsername("testuser"),
			tcpostgres.WithPassword("testpassword"),
			testcontainers.WithWaitStrategy(
				wait.ForLog("database system is ready to accept connections").
					WithOccurrence(2).
					WithStartupTimeout(60*time.Second),
			),
		)
		if err != nil {
			t.Fatalf("failed to start postgres testcontainer: %v", err)
		}
		container = pgC

		connStr, err := pgC.ConnectionString(ctx, "sslmode=disable")
		if err != nil {
			_ = pgC.Terminate(ctx)
			t.Fatalf("failed to get connection string from testcontainer: %v", err)
		}
		dsn = connStr
	}

	database, err := db.New(dsn, db.PoolConfig{
		MaxOpenConns:    10,
		MaxIdleConns:    2,
		ConnMaxLifetime: 5 * time.Minute,
		SlowThreshold:   50 * time.Millisecond,
	})
	if err != nil {
		if container != nil {
			_ = container.Terminate(ctx)
		}
		t.Fatalf("failed to connect to test database: %v", err)
	}

	testDB := &TestDatabase{
		DB:        database,
		DSN:       dsn,
		container: container,
	}

	if err := testDB.applyMigrations(ctx); err != nil {
		testDB.Close()
		t.Fatalf("failed to apply migrations to test database: %v", err)
	}
	if envDSN != "" {
		testDB.Truncate(t)
	}

	t.Cleanup(func() {
		testDB.Close()
	})

	return testDB
}

func (td *TestDatabase) applyMigrations(ctx context.Context) error {
	migrationFiles := []string{
		"migrations/000001_init.up.sql",
	}

	for _, file := range migrationFiles {
		sqlBytes, err := db.MigrationsFS.ReadFile(file)
		if err != nil {
			return fmt.Errorf("read migration file %s: %w", file, err)
		}

		if _, err := td.DB.Pool.ExecContext(ctx, string(sqlBytes)); err != nil {
			return fmt.Errorf("execute migration %s: %w", file, err)
		}
	}

	return nil
}

func ReadMigrationFile(file string) ([]byte, error) {
	return db.MigrationsFS.ReadFile(file)
}

func (td *TestDatabase) Close() {
	if td.DB != nil {
		td.DB.Close()
	}
	if td.container != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = td.container.Terminate(ctx)
	}
}

func (td *TestDatabase) Truncate(t testing.TB) {
	t.Helper()
	ctx := context.Background()

	tables := []string{
		"public.outbox_events",
		"public.idempotency_keys",
		"public.audit_logs",
		"public.task_logs",
		"public.tasks",
		"iam.sessions",
		"iam.users",
	}

	for _, table := range tables {
		_, _ = td.DB.Pool.ExecContext(ctx, fmt.Sprintf("TRUNCATE TABLE %s CASCADE", table))
	}
}

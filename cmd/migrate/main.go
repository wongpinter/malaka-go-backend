package main

import (
	"context"
	"fmt"
	"os"

	"malaka/internal/config"
	"malaka/internal/platform/db"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "migrate: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	cfg := config.Get()
	if err := cfg.Validate(); err != nil {
		return err
	}

	database, err := db.Open(db.Driver(cfg.DBDriver), cfg.DBURL, db.PoolConfig{
		MaxOpenConns:    cfg.DBMaxOpenConns,
		MaxIdleConns:    cfg.DBMaxIdleConns,
		ConnMaxLifetime: cfg.DBConnLifetime,
	})
	if err != nil {
		return err
	}
	defer database.Close()

	if database.Driver() == db.DriverSQLite {
		fmt.Println("applied sqlite schema")
		return nil
	}

	statements, err := db.MigrationStatements()
	if err != nil {
		return err
	}
	ctx := context.Background()
	for i, stmt := range statements {
		if _, err := database.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("statement %d: %w", i+1, err)
		}
	}
	fmt.Printf("applied %d postgres statements\n", len(statements))
	return nil
}

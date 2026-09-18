package db

import (
	"context"
	"database/sql"
)

type Driver string

const (
	DriverPostgres Driver = "postgres"
	DriverSQLite   Driver = "sqlite"
)

type Executor interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	PrepareContext(context.Context, string) (*sql.Stmt, error)
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

type Tx interface {
	Executor
	Commit() error
	Rollback() error
	Driver() Driver
}

type Database interface {
	Executor
	Ping(context.Context) error
	Close()
	Raw() *sql.DB
	Driver() Driver
	ExecTx(context.Context, func(Tx) error) error
}

func DriverOf(value any) Driver {
	if d, ok := value.(interface{ Driver() Driver }); ok {
		return d.Driver()
	}
	return DriverPostgres
}

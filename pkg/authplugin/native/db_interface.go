package native

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// DBPool is an interface that abstracts database operations for testing.
// It matches the subset of pgxpool.Pool methods used by the native managers.
type DBPool interface {
	// Query executes a query that returns rows
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	
	// QueryRow executes a query that returns at most one row
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	
	// Exec executes a query that doesn't return rows
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	
	// Close closes the pool
	Close()
}

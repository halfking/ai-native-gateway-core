package probelane

import (
	"context"
	"fmt"
	"log/slog"
)

// DBRow is the Scan half of pgx.Row (keeps this package free of a pgx import).
type DBRow interface {
	Scan(dest ...any) error
}

// DB is the QueryRow surface needed by AssertLaneTablesReady.
type DB interface {
	QueryRow(ctx context.Context, sql string, args ...any) DBRow
}

type queryRowFunc func(ctx context.Context, sql string, args ...any) DBRow

func (f queryRowFunc) QueryRow(ctx context.Context, sql string, args ...any) DBRow {
	return f(ctx, sql, args...)
}

// AssertWithQueryRow adapts any QueryRow that returns a Scan-able row
// (e.g. (*pgxpool.Pool).QueryRow) without forcing an import cycle on pgx.
func AssertWithQueryRow(ctx context.Context, queryRow func(ctx context.Context, sql string, args ...any) DBRow) error {
	return AssertLaneTablesReady(ctx, queryRowFunc(queryRow))
}

// AssertLaneTablesReady checks that physical-lane tables exist when the flag
// is on. Missing tables + flag on would make PersistTurn fail every probe and
// silently drop firePersistedHooks (stats / live / mirror).
//
// When the flag is off this is a no-op (tables may or may not exist).
func AssertLaneTablesReady(ctx context.Context, db DB) error {
	if !PhysicalEnabled() {
		return nil
	}
	if db == nil {
		return fmt.Errorf("probelane: AssertLaneTablesReady called with nil db while %s is on", EnvPhysical)
	}
	for _, table := range []string{"session_turns_probe_hot", "request_stats_probe_minute"} {
		var name *string
		if err := db.QueryRow(ctx, `SELECT to_regclass($1)::text`, "public."+table).Scan(&name); err != nil {
			return fmt.Errorf("probelane: probe table check failed for %s: %w", table, err)
		}
		if name == nil || *name == "" {
			return fmt.Errorf("probelane: %s is on but public.%s is missing — apply migration 928 before enabling the lane", EnvPhysical, table)
		}
	}
	slog.Info("probelane: physical lane ready",
		"env", EnvPhysical,
		"tables", "session_turns_probe_hot,request_stats_probe_minute")
	return nil
}

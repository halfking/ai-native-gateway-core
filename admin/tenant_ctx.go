package admin

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// withTenantTx wraps a callback in a read-only transaction that sets the
// app.current_tenant GUC. This makes RLS policies on request_logs,
// tenant_tool_policies, tenant_model_policies etc. enforce tenant isolation
// as defense-in-depth on top of the application-level WHERE tenant_id filter.
//
// Usage:
//
//	err := withTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
//	    rows, err := tx.Query(ctx, "SELECT ... FROM request_logs WHERE ...")
//	    // process rows
//	    return err
//	})
//
// The GUC is set via SET LOCAL (transaction-scoped), so it auto-clears on
// commit/rollback. Single quotes in tenantID are escaped to prevent injection.
func withTenantTx(ctx context.Context, pool *pgxpool.Pool, tenantID string, fn func(tx pgx.Tx) error) error {
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return fmt.Errorf("begin tenant tx: %w", err)
	}
	//nolint:errcheck // deferred rollback, best-effort
	defer tx.Rollback(ctx)

	escaped := strings.ReplaceAll(tenantID, "'", "''")
	if _, err := tx.Exec(ctx, "SET LOCAL app.current_tenant = '"+escaped+"'"); err != nil {
		return fmt.Errorf("set tenant GUC: %w", err)
	}

	if err := fn(tx); err != nil {
		return err
	}

	return tx.Commit(ctx)
}

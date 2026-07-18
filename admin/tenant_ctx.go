package admin

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// withTenantTx runs a read-only callback with tenant RLS context. A non-empty
// tenant sets app.current_tenant; all-tenant callers use the explicit bypass
// helper below and must already be authenticated as super_admin/admin_key.
func withTenantTx(ctx context.Context, pool *pgxpool.Pool, tenantID string, fn func(tx pgx.Tx) error) error {
	return withReadOnlyTx(ctx, pool, func(tx pgx.Tx) error {
		if err := setLocalTenantGUC(ctx, tx, tenantID); err != nil {
			return err
		}
		return fn(tx)
	})
}

func withAllTenantReadOnlyTx(ctx context.Context, pool *pgxpool.Pool, fn func(tx pgx.Tx) error) error {
	return withReadOnlyTx(ctx, pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SELECT set_config('app.current_role', 'super_admin', true)"); err != nil {
			return fmt.Errorf("set super-admin role GUC: %w", err)
		}
		if _, err := tx.Exec(ctx, "SELECT set_config('app.bypass_rls', 'true', true)"); err != nil {
			return fmt.Errorf("set RLS bypass GUC: %w", err)
		}
		return fn(tx)
	})
}

func withReadOnlyTx(ctx context.Context, pool *pgxpool.Pool, fn func(tx pgx.Tx) error) error {
	if pool == nil {
		return fmt.Errorf("nil database pool")
	}
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return fmt.Errorf("begin read-only tx: %w", err)
	}
	defer tx.Rollback(ctx)
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func withTenantQueryRow(ctx context.Context, pool *pgxpool.Pool, tenantID string, fn func(tx pgx.Tx) error) error {
	return withTenantTx(ctx, pool, tenantID, fn)
}

func setLocalTenantGUC(ctx context.Context, tx pgx.Tx, tenantID string) error {
	if strings.TrimSpace(tenantID) == "" {
		return fmt.Errorf("empty tenant_id for SET LOCAL app.current_tenant")
	}
	if _, err := tx.Exec(ctx, "SELECT set_config('app.current_tenant', $1, true)", tenantID); err != nil {
		return fmt.Errorf("set tenant GUC: %w", err)
	}
	return nil
}

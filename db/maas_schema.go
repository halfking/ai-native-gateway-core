package db

import (
	"context"
	_ "embed"
	"log/slog"
)

//go:embed migrations/007_maas_billing.sql
var maasBillingSchemaSQL string

//go:embed migrations/008_billing_orders.sql
var maasBillingOrdersSQL string

//go:embed migrations/009_request_logs_tenant_backfill.sql
var requestLogsTenantBackfillSQL string

func (d *DB) EnsureMaasSchema(ctx context.Context) error {
	if d == nil || d.pool == nil {
		return nil
	}
	if _, err := d.pool.Exec(ctx, maasBillingSchemaSQL); err != nil {
		return err
	}
	if _, err := d.pool.Exec(ctx, maasBillingOrdersSQL); err != nil {
		return err
	}
	if _, err := d.pool.Exec(ctx, requestLogsTenantBackfillSQL); err != nil {
		return err
	}
	// Ensure default tenant has a wallet row.
	_, _ = d.pool.Exec(ctx, `
		INSERT INTO tenant_credit_wallets (tenant_id)
		SELECT code FROM tenants
		ON CONFLICT (tenant_id) DO NOTHING
	`)
	slog.Info("maas billing schema ensured (settings, plans, wallets, ledger, orders, three-pool)")
	return nil
}

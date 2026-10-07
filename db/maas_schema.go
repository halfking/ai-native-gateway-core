package db

import (
	"context"
	"log/slog"
)

// 2026-10-07 (audit §10.97): `ALTER TABLE … ADD COLUMN IF NOT EXISTS` does NOT
// spare the lock. PostgreSQL must take ACCESS EXCLUSIVE on the parent table and
// on every partition before it can check whether the column exists, so a column
// that has been in place for months still costs a full exclusive pass on every
// boot. On production this statement measured 420 calls with a mean of 2,289 ms
// and a worst single call of 104,322 ms — against request_logs, the busiest
// table in the database, with 5 monthly partitions there (6 relations locked)
// for a statement that changes nothing. That is 16 minutes of cumulative
// blocking on the hottest table over a 25.9 day window.
//
// The same shape is applied to work_type in db.go (workTypeRequestLogsCurrent);
// this is the remaining unguarded request_logs DDL in the startup path.
const maasRequestLogsDDL = `
		ALTER TABLE request_logs
		    ADD COLUMN IF NOT EXISTS credits_charged BIGINT;

		CREATE INDEX IF NOT EXISTS idx_request_logs_credits_charged
		    ON request_logs (tenant_id, ts DESC)
		    WHERE credits_charged IS NOT NULL AND credits_charged > 0;`

// maasRequestLogsCurrent reports whether request_logs already carries the
// credits_charged column and its index, i.e. whether maasRequestLogsDDL would
// be a pure no-op. Both are checked because the DDL is applied as one unit: the
// index cannot be created before the column exists.
func (d *DB) maasRequestLogsCurrent(ctx context.Context) bool {
	var missing int
	err := d.pool.QueryRow(ctx, `
		SELECT
		  (SELECT count(*) FROM (VALUES ('credits_charged')) AS want(name)
		   WHERE to_regclass('public.request_logs') IS NOT NULL
		     AND NOT EXISTS (
		       SELECT 1 FROM information_schema.columns
		        WHERE table_schema='public' AND table_name='request_logs'
		          AND column_name = want.name))
		  +
		  (SELECT count(*) FROM (VALUES ('idx_request_logs_credits_charged')) AS want(name)
		   WHERE NOT EXISTS (
		       SELECT 1 FROM pg_indexes
		        WHERE schemaname='public' AND indexname = want.name))
	`).Scan(&missing)
	if err != nil {
		// A failed probe must not be read as "current": that would skip DDL on
		// a database that genuinely needs it. Fall through to applying it.
		slog.Warn("maas request_logs probe failed; applying DDL", "error", err)
		return false
	}
	return missing == 0
}

// EnsureMaasSchema applies MaaS billing tables and pricing v2 columns (idempotent).
func (d *DB) EnsureMaasSchema(ctx context.Context) error {
	if d == nil || d.pool == nil {
		return nil
	}
	if d.maasRequestLogsCurrent(ctx) {
		slog.Info("maas schema ensured (pricing v2 columns); request_logs credits_charged " +
			"column and index already present, DDL skipped")
	} else if _, err := d.pool.Exec(ctx, maasRequestLogsDDL); err != nil {
		return err
	}
	_, err := d.pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS maas_settings (
		    id INT PRIMARY KEY DEFAULT 1 CHECK (id = 1),
		    cents_per_credit NUMERIC(10, 4) NOT NULL DEFAULT 0.1,
		    base_credits_per_1m BIGINT NOT NULL DEFAULT 10000,
		    currency_display VARCHAR(8) NOT NULL DEFAULT 'CNY',
		    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
		);
		INSERT INTO maas_settings (id) VALUES (1) ON CONFLICT (id) DO NOTHING;

		ALTER TABLE maas_settings
		    ADD COLUMN IF NOT EXISTS base_credits_per_1m_out BIGINT,
		    ADD COLUMN IF NOT EXISTS base_credits_per_1m_cache_in BIGINT,
		    ADD COLUMN IF NOT EXISTS base_credits_per_1m_cache_out BIGINT,
		    ADD COLUMN IF NOT EXISTS global_discount NUMERIC(6, 4) NOT NULL DEFAULT 1.0;

		UPDATE maas_settings SET
		    base_credits_per_1m_out = COALESCE(base_credits_per_1m_out, base_credits_per_1m),
		    base_credits_per_1m_cache_in = COALESCE(base_credits_per_1m_cache_in, base_credits_per_1m),
		    base_credits_per_1m_cache_out = COALESCE(base_credits_per_1m_cache_out, base_credits_per_1m)
		WHERE id = 1;

		CREATE TABLE IF NOT EXISTS model_credit_rates (
		    canonical_id INT PRIMARY KEY REFERENCES models_canonical(id) ON DELETE CASCADE,
		    credits_per_1m_in BIGINT,
		    credits_per_1m_out BIGINT,
		    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
		);

		ALTER TABLE model_credit_rates
		    ADD COLUMN IF NOT EXISTS credits_per_1m_cache_in BIGINT,
		    ADD COLUMN IF NOT EXISTS credits_per_1m_cache_out BIGINT,
		    ADD COLUMN IF NOT EXISTS manual_in BOOLEAN NOT NULL DEFAULT FALSE,
		    ADD COLUMN IF NOT EXISTS manual_out BOOLEAN NOT NULL DEFAULT FALSE,
		    ADD COLUMN IF NOT EXISTS manual_cache_in BOOLEAN NOT NULL DEFAULT FALSE,
		    ADD COLUMN IF NOT EXISTS manual_cache_out BOOLEAN NOT NULL DEFAULT FALSE;

		UPDATE model_credit_rates SET
		    manual_in = TRUE,
		    manual_out = TRUE
		WHERE (credits_per_1m_in IS NOT NULL OR credits_per_1m_out IS NOT NULL)
		  AND NOT (manual_in OR manual_out OR manual_cache_in OR manual_cache_out);
	`)
	if err != nil {
		return err
	}
	slog.Info("maas schema ensured (pricing v2 columns)")
	return nil
}

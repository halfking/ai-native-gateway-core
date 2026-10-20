package db

import (
	"context"
	"fmt"
	"log/slog"
)

// 2026-10-07 (audit §10.97): `ALTER TABLE … ADD COLUMN IF NOT EXISTS` does NOT
// spare the lock. PostgreSQL must take ACCESS EXCLUSIVE on the parent table and
// on every partition before it can check whether the column exists, so a column
// that has been in place for months still costs a full exclusive pass on every
// boot. On production this statement measured 420 calls with a mean of 2,289 ms
// and a worst single call of 104,322 ms — against _request_logs, the busiest
// table in the database, with 5 monthly partitions there (6 relations locked)
// for a statement that changes nothing. That is 16 minutes of cumulative
// blocking on the hottest table over a 25.9 day window.
//
// The same shape is applied to work_type in db.go (workTypeRequestLogsCurrent);
// this is the remaining unguarded _request_logs DDL in the startup path.
const maasRequestLogsDDL = `
		ALTER TABLE _request_logs
		    ADD COLUMN IF NOT EXISTS credits_charged BIGINT;

		CREATE INDEX IF NOT EXISTS idx_request_logs_credits_charged
		    ON _request_logs (tenant_id, ts DESC)
		    WHERE credits_charged IS NOT NULL AND credits_charged > 0;`

// maasRequestLogsCurrent reports whether _request_logs already carries the
// credits_charged column and its index, i.e. whether maasRequestLogsDDL would
// be a pure no-op. Both are checked because the DDL is applied as one unit: the
// index cannot be created before the column exists.
func (d *DB) maasRequestLogsCurrent(ctx context.Context) bool {
	var missing int
	err := d.pool.QueryRow(ctx, `
		SELECT
		  (SELECT count(*) FROM (VALUES ('credits_charged')) AS want(name)
		   WHERE to_regclass('public._request_logs') IS NOT NULL
		     AND NOT EXISTS (
		       SELECT 1 FROM information_schema.columns
		        WHERE table_schema='public' AND table_name='_request_logs'
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
		slog.Warn("maas _request_logs probe failed; applying DDL", "error", err)
		return false
	}
	return missing == 0
}

// EnsureMaasSchema applies MaaS billing tables and pricing v2 columns (idempotent).
func (d *DB) EnsureMaasSchema(ctx context.Context) error {
	if d == nil || d.pool == nil {
		return nil
	}
	// ★ 885（v1 族退役）：族不存在 ⇒ 这段 DDL 不是「no-op」而是 **42P01**
	// （ALTER TABLE 一个不存在的表）。
	//
	// ⚠ 为什么 catalog 短路救不了：短路算的是「**缺多少**」——列项因
	// to_regclass 为 NULL 而计 0，**索引项计 1** ⇒ missing ≠ 0 ⇒ 判定
	// 「不 current」⇒ 继续执行 DDL ⇒ 42P01 ⇒ 整个 EnsureMaasSchema 失败 ⇒
	// boot 失败 ⇒ 网关「就绪但没有数据库」（readyz 在 SetDepsOptional(true)
	// 下仍返 200）。
	// ⇒ 判据必须从「缺什么」改成「**在不在**」，与 ensureRequestLogSchema /
	// ensureWorkTypeSchema 同一条纪律。
	var v1TablePresent bool
	if err := d.pool.QueryRow(ctx, `
		SELECT to_regclass('public._request_logs') IS NOT NULL
	`).Scan(&v1TablePresent); err != nil {
		return fmt.Errorf("probe v1 request_logs presence for maas DDL: %w", err)
	}
	if !v1TablePresent {
		slog.Info("v1 request_logs family is retired (885) — maas credits_charged DDL skipped")
	} else if d.maasRequestLogsCurrent(ctx) {
		slog.Info("maas schema ensured (pricing v2 columns); _request_logs credits_charged " +
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

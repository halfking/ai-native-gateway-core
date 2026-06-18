package db

import (
	"context"
	"log/slog"
)

// EnsureMaasSchema applies MaaS billing tables and pricing v2 columns (idempotent).
func (d *DB) EnsureMaasSchema(ctx context.Context) error {
	if d == nil || d.pool == nil {
		return nil
	}
	_, err := d.pool.Exec(ctx, `
		ALTER TABLE request_logs
		    ADD COLUMN IF NOT EXISTS credits_charged BIGINT;

		CREATE INDEX IF NOT EXISTS idx_request_logs_credits_charged
		    ON request_logs (tenant_id, ts DESC)
		    WHERE credits_charged IS NOT NULL AND credits_charged > 0;

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

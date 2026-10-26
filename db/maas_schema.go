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
	// ★ 2026-10-24（v1 → session_turns 替换轮）：本函数原来还有一条 v1 腿 ——
	// 给 _request_logs 补 credits_charged 列 + idx_request_logs_credits_charged
	// 索引。那条腿随 **890（v1 族退役）**失效：族不存在 ⇒ ALTER 一张不存在
	// 的表 = 42P01 ⇒ 整个 EnsureMaasSchema 失败 ⇒ boot 失败 ⇒ 网关
	// 「就绪但没有数据库」（readyz 在 SetDepsOptional(true) 下仍返 200）。
	// 它此前靠一道显式存在性闸 + catalog 短路挡着。
	//
	// ⇒ 现在 v1 腿整条删除：credits_charged 由**会话族**提供
	//   （session_turns.credits_charged，由迁移链建；canonical 视图按 v1
	//   同名列投影 ⇒ maas.RequestLogCreditsSQL 与所有读方零改动）。
	// ⇒ 只保留下面这批定价表（maas_settings / model_credit_rates）——
	//   它们不是 v1 族，且是计费的价格来源。
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

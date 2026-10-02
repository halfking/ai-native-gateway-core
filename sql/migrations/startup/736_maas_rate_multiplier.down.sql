-- 736 down (Wave 3 B1, 2026-09-22): 移除峰谷倍率列与共享取档函数。
-- 历史行携带的倍率信息随之丢失——down 前如需保留对账证据，请先导出
-- usage_ledger.rate_multiplier ≠ 1.0 的行。

DROP FUNCTION IF EXISTS maas_resolve_rate_multiplier(timestamptz);

ALTER TABLE usage_ledger DROP COLUMN IF EXISTS rate_multiplier;
ALTER TABLE usage_ledger_hot DROP COLUMN IF EXISTS rate_multiplier;
ALTER TABLE request_logs DROP COLUMN IF EXISTS credits_rate_multiplier;
ALTER TABLE request_logs_hot DROP COLUMN IF EXISTS credits_rate_multiplier;

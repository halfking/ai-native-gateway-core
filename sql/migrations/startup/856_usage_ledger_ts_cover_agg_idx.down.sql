-- 856_usage_ledger_ts_cover_agg_idx.down.sql
-- 回滚 856：只删索引，不动任何数据。
--
-- 删掉后，/api/usage/summary 会退回 856 上线前的形态：
--   对 usage_ledger_2026_10 的 66,431 行逐行回堆取 5 个指标列。
-- 回滚判据：usage summary 的 EXPLAIN ANALYZE Execution Time
--   在 856 上线后**没有**明显下降，且索引带来的写放大已被证明有害。

BEGIN;

DROP INDEX IF EXISTS public.usage_ledger_ts_tenant_cover_agg_idx;
DROP INDEX IF EXISTS public.usage_ledger_hot_ts_tenant_cover_agg_idx;

COMMIT;
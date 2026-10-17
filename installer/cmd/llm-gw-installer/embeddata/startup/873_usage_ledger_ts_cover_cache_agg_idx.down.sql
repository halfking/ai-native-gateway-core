-- Rollback for migration 873_usage_ledger_ts_cover_cache_agg_idx.sql.
-- Drops the superset covering indexes and recreates 856's originals verbatim.
-- No data is touched.
--
-- ⚠ CONCURRENTLY note: this runs inside the channel's single-transaction chain,
-- so it cannot use DROP INDEX CONCURRENTLY. Dropping an index takes an
-- ACCESS EXCLUSIVE lock on the parent (and cascades to partitions), which
-- blocks writes for the duration of the (metadata-only) drop — normally
-- milliseconds. The **re-create** at the bottom is the slow part (it rebuilds
-- the whole index), and it holds ShareLock per partition while it runs.
--
-- ⚠ 回滚代价是**功能退化，不是故障恢复**：856 那条索引不含缓存 token 列，
--   回滚后 cache-economics 这类带 SUM(cache_read_tokens) 的窗口聚合会重新
--   退回逐行回堆（实测 30 天窗口 buffers 19,245 → 209,361）。确认是索引
--   本身的问题（而不是计划选择）再回滚，否则会把已知缺陷重新引回来。

BEGIN;

SET LOCAL statement_timeout = '10min';
SET LOCAL lock_timeout = '2s';

DROP INDEX IF EXISTS public.usage_ledger_ts_tenant_cache_agg_idx;
DROP INDEX IF EXISTS public.usage_ledger_hot_ts_tenant_cache_agg_idx;

-- 856 原样恢复（含它当时的 INCLUDE 列表，不含缓存 token 列）。
CREATE INDEX IF NOT EXISTS usage_ledger_ts_tenant_cover_agg_idx
    ON public.usage_ledger (ts, tenant_id)
    INCLUDE (prompt_tokens, completion_tokens, cost_usd, latency_ms, success);

CREATE INDEX IF NOT EXISTS usage_ledger_hot_ts_tenant_cover_agg_idx
    ON public.usage_ledger_hot (ts, tenant_id)
    INCLUDE (prompt_tokens, completion_tokens, cost_usd, latency_ms, success);

COMMIT;

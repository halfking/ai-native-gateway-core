-- Migration 904 down: 删除 inspector-stats 两条聚合腿的部分索引。
--
-- ★ 回滚后 GET /api/admin/sessions/inspector-stats 立刻回到慢形态：
--   L2（AVG health_score）从 119.706 ms 回到 **389.373 ms**
--     （Parallel Index Only Scan → Parallel Seq Scan + Filter，
--       Rows Removed by Filter: 184,361）
--   L3（今日关闭数）从 0.100 ms 回到 **195.432 ms**
--     （Index Only Scan → Parallel Seq Scan + Filter，
--       Rows Removed by Filter: 301,024）
--   （以上均为 2026-10-27 252 生产 EXPLAIN ANALYZE 实测）
-- ⇒ 这是**一步性能回退**（如实登记，不是缺陷）。

BEGIN;

DROP INDEX IF EXISTS public.idx_session_dim_closed_at_closed;
DROP INDEX IF EXISTS public.idx_session_summaries_health_score_notnull;

COMMIT;

-- POST_CONDITION:
--   SELECT count(*) FROM pg_indexes
--    WHERE schemaname = 'public'
--      AND indexname IN ('idx_session_dim_closed_at_closed',
--                         'idx_session_summaries_health_score_notnull');
--   -- 预期：0

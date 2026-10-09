-- dbinit:no-transaction —— 本文件含 CREATE INDEX CONCURRENTLY，PostgreSQL 不允许
-- 在事务块内执行；installer 的 applySQL 据此对本文件走非事务通道（见
-- installer/internal/dbinit/runner.go noTransactionMarker）。无该标记时全新
-- 安装会失败于 "CREATE INDEX CONCURRENTLY cannot run inside a transaction block"。
-- ===========================================================================
-- File:          sql/migrations/startup/845_stats_event_dedup_join_index.sql
-- Migration:     845
-- Database:      llm_gateway
-- Purpose:       给 stats_event_dedup 补 (event_id, occurred_at) 复合索引，
--                消除 stats 日聚合 JOIN 的逐行 heap 回表
--
-- Status:        active
-- Idempotent:    YES (CREATE INDEX CONCURRENTLY IF NOT EXISTS)
-- Dependencies:  536_stats_analytics_foundation.sql（stats_event_dedup 建表，
--                现有唯一索引为 stats_event_dedup_pkey(event_id)）
--
-- Background:
--   2026-10-09 252 PG SQL 日志审计第 30 轮
--   (docs/audit/2026-10-09-252-sql-log-audit-round30.md)。
--   domains/stats/daily_monthly_rollup.go dailyInsertSQL（每实例每小时 tick，
--   重建最近 48h daily 桶）：
--     WITH latest AS (
--       SELECT f.* FROM usage_facts f
--       JOIN stats_event_dedup d
--         ON d.event_id = f.event_id AND d.occurred_at = f.occurred_at
--       WHERE f.occurred_at >= $1 AND f.occurred_at < $2)
--   在 252 真库 24h 窗内 ≥1s 慢查 21 条（max 29.5s）+ 30s statement_timeout
--   击杀 36 次 —— refreshRecent 的 2min context 还没到，角色级 30s rolconfig
--   先杀，击杀后整 tick 报废、下小时重试，36/48 次 tick 没有产出。
--
--   252 实测（EXPLAIN ANALYZE，2h 窗 2,208 行）：总耗时 2,285ms，其中
--   stats_event_dedup_pkey 探测 loops=2,208 × 1.677ms/loop = 99.7%。
--   根因：dedup 表只有 pkey(event_id)，JOIN 条件的 occurred_at 只能做
--   Filter（回 heap 取行后比较）。914,685 行的 heap 页冷读 ⇒ 每次探测
--   ~1.7ms 随机 IO；48h 窗 ~28,429 行外推 30-48s，与 30s 击杀边缘吻合
--   （max 29.5s = 热缓存后的下界）。
--
--   修复：补 (event_id, occurred_at) 复合索引，两列都在索引内 ⇒
--   Index Only Scan，Filter 卸载到索引层，随机 heap 回表清零。
--   2h 窗实测外推 48h 窗 < 2s（原 30-48s）。
--
--   写路径无变化：event_writer.go 对 dedup 只有
--   INSERT ... ON CONFLICT (event_id) DO UPDATE，索引维护成本可忽略；
--   该表无 TTL（914,685 行持续增长，见审计轮登记项）。
-- ===========================================================================

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_stats_event_dedup_event_id_occurred_at
    ON public.stats_event_dedup (event_id, occurred_at);

COMMENT ON INDEX idx_stats_event_dedup_event_id_occurred_at IS
    '845: stats 日聚合 dailyInsertSQL JOIN (event_id, occurred_at) 的承重索引 —— 消除 pkey(event_id) 探测后按 occurred_at 回 heap Filter 的逐行随机 IO；两列在索引内转 Index Only Scan。';

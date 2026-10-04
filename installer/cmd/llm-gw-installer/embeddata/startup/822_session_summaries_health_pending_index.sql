-- dbinit:no-transaction —— 本文件含 CREATE INDEX CONCURRENTLY，PostgreSQL 不允许
-- 在事务块内执行；installer 的 applySQL 据此对本文件走非事务通道（见
-- installer/internal/dbinit/runner.go noTransactionMarker）。无该标记时全新
-- 安装会失败于 "CREATE INDEX CONCURRENTLY cannot run inside a transaction block"。
-- ===========================================================================
-- File:          sql/migrations/startup/822_session_summaries_health_pending_index.sql
-- Migration:     822
-- Database:      llm_gateway
-- Purpose:       给 session_summaries 的健康分回填捞取查询补部分索引
--                (last_request_at DESC) WHERE health_score IS NULL
--
-- Status:        active
-- Idempotent:    YES (CREATE INDEX CONCURRENTLY IF NOT EXISTS)
-- Dependencies:  baseline session_summaries 建表（health_score / last_request_at 列）
--
-- Background:
--   2026-10-04 252 PG SQL 日志审计第 23 轮
--   (docs/audit/2026-10-04-252-sql-log-audit-round23.md)。bg/session_health_worker.go
--   的 sweep 捞取查询（每实例 60min 一 tick，10-02 ece68f148 接入生产）：
--     SELECT ... FROM session_summaries
--     WHERE last_request_at < NOW() - INTERVAL '1 hour'
--       AND health_score IS NULL
--     ORDER BY last_request_at DESC LIMIT 100
--   在 252 真库上 EXPLAIN 为 Parallel Seq Scan + Sort（586,339 行全扫，
--   其中 394,710 行 health_score IS NULL），单次 25.8-29.4s（>1s 慢日志
--   24h 28 条，夜间每实例每小时稳定命中）。现有 20 个索引无一覆盖
--   「health_score IS NULL + last_request_at 排序」组合（多为 tenant 前导
--   列，本查询无 tenant 谓词），planner 无路可选。
--
--   部分索引只含待评分行，批处理完成后行即离开索引，体积随积压收敛；
--   查询按 last_request_at DESC 走 index scan 取 top-100，最近 1h 内的
--   新会话（尚未满足 <now()-1h 谓词）只产生少量 index entry 跳过成本。
--
--   锁与失败模式（对齐 807 惯例）：CONCURRENTLY 不阻塞在途读写；失败可能
--   留下 INVALID 索引，复跑本迁移即可清除（IF NOT EXISTS 对 INVALID 索引
--   同样命中，先 DROP 后建由复跑语义覆盖）。
-- ===========================================================================

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_session_summaries_health_pending
    ON public.session_summaries (last_request_at DESC)
    WHERE health_score IS NULL;

COMMENT ON INDEX idx_session_summaries_health_pending IS
    '822: bg/session_health_worker sweep 捞取查询的承重索引 —— health_score IS NULL 待评分行按 last_request_at DESC 取 top-100；行被评分后即离开本部分索引。';

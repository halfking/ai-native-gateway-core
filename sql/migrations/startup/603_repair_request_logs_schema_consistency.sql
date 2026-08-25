-- ===========================================================================
-- File:          sql/migrations/startup/603_repair_request_logs_schema_consistency.sql
-- Migration:     603
-- Database:      llm_gateway
-- Purpose:       修复 request_logs 与 request_logs_hot 的 schema 不一致
--
-- Status:        active
-- Idempotent:    YES (全部 IF EXISTS / IF NOT EXISTS + 单事务包裹)
-- Dependencies:  573_drop_request_logs_body_columns (必须在 252 上成功执行)
--
-- Background:
--   1. 252 PG (172.16.2.210 llm_gateway) 与本地对比报告（docs/audits/2026-08-25-252-pg-schema-consistency-report.md）
--      揭示 request_logs vs request_logs_hot 有 25 处差异。
--   2. 根因：Migration 341 (2026-07-05) 创建独立 request_logs_hot 时使用
--      `LIKE request_logs INCLUDING DEFAULTS` 但 hot 表不继承后续 ALTER TABLE。
--   3. Migration 420/485/487/543 增加了多列但没同步到 hot 表（hot 通过单独的
--      428/445/449/484/488/491 迁移独立演进），形成 schema 漂移。
--   4. Migration 573 在 252 上"半成功"：hot 表的 body 列 DROP 了，
--      parent 表的 outbound_body 因 view 重建失败事务回滚而残留（已修复）。
--
-- Differences Fixed (本迁移):
--   A. DROP parent.request_logs.outbound_body (残留列，0 行，view 不再引用)
--   B-K. 同步 hot 表缺失的 10 列（与 parent 保持一致）
--
-- Differences Documented (NOT Fixed, accepted as design divergence):
--   - HOT_ONLY in request_logs_hot: caller_id (341), session_correlation_id (428),
--     status_code (484) — hot 表独立演进字段，业务代码不依赖
--   - Type drift in request_logs_hot vs request_logs:
--     * agent_name varchar(255) vs text
--     * agent_type varchar(50) vs text
--     * task_id varchar(255) vs text
--     * api_key_fingerprint varchar(16) vs text
--     * content_safety_score jsonb vs double precision
--     * dlp_violations jsonb vs text[]
--     * protocol_conversion boolean vs text
--     * ir_extensions jsonb vs text
--     * sanitizer_mutations jsonb vs text
--     (hot 表是独立设计简化版本；类型简化是 hot 表的"主动设计"，
--      业务代码不读写这些字段，602 promote 用 explicit column list 规避)
--   - Default value drift: request_logs_hot.attachment_count DEFAULT 0,
--     has_attachments DEFAULT false (parent 表无默认值, hot 表防御性默认值)
--   - Nullable drift: candidate_failure_logs_hot.ts nullable=YES vs parent NO
--     (hot 表允许补录无 ts 的历史数据)
--
-- View considerations (rule 49 §9.1, §9.2):
--   - 603 只改 hot 表 ADD COLUMN + parent 表 DROP COLUMN outbound_body
--   - 两个 view（request_logs_with_current_month +
--     request_logs_with_current_month_without_customer_id）都用 explicit
--     column list，列名固定 108 列；hot 多出的 45 列和 parent 少的 3 列都不在 view
--     内，因此无需重建 view
--   - 验证（2026-08-25 19:40 CST）：view 列对齐 hot=108 / parent=108 ✅
--
-- Estimated Time:   ~5 minutes (加列无锁，秒级)
-- Affected Rows:    0 (parent 表已空，hot 表 ALTER 仅加列不写数据)
-- Breaking Change:  NO
-- Rollback Script:  603_repair_request_logs_schema_consistency.down.sql
--
-- Safety Check:
--   - 252 上 parent.request_logs 是空表 (清理后 0 行)
--   - 252 上 hot 表 ~25K 行，加列是 PG 11+ 的 fast non-blocking default
--   - 加列后无写路径立即填充，业务代码需要按需使用新列
--
-- Date: 2026-08-25
-- Author: ZCode (handoff 接续 252 清理任务)
-- Refs: docs/audits/2026-08-25-252-pg-schema-consistency-report.md
-- ===========================================================================

\set ON_ERROR_STOP on

-- ─── 单事务包裹 ───
-- 全部 DDL 在一个 BEGIN/COMMIT 内：要么全部成功要么全部失败，
-- 避免 ALTER TABLE 多次提交造成半成功状态。
BEGIN;

-- ─── A. DROP parent 表的 outbound_body 残留列 ───
-- 573 在 252 上半成功：hot 的 body 列没了，但 parent 因 view 重建失败事务回滚残留
-- 防御性：即使 573 已经处理，再次确保
ALTER TABLE public.request_logs DROP COLUMN IF EXISTS outbound_body;

-- ─── B-K. 同步 hot 表缺失的 10 列（与 parent 保持一致）───
-- 所有加列都使用 IF NOT EXISTS 幂等，类型与 parent 表定义严格一致
-- PG 11+ 加列 fast path：仅更新 catalog，不重写表数据
ALTER TABLE public.request_logs_hot ADD COLUMN IF NOT EXISTS cached_response_id BIGINT;
ALTER TABLE public.request_logs_hot ADD COLUMN IF NOT EXISTS context_size_tokens INTEGER;
ALTER TABLE public.request_logs_hot ADD COLUMN IF NOT EXISTS continuation_keywords TEXT[];
ALTER TABLE public.request_logs_hot ADD COLUMN IF NOT EXISTS effective_timeout_seconds INTEGER;
ALTER TABLE public.request_logs_hot ADD COLUMN IF NOT EXISTS is_continuation BOOLEAN DEFAULT FALSE;
ALTER TABLE public.request_logs_hot ADD COLUMN IF NOT EXISTS keepalive_sent_count INTEGER DEFAULT 0;
ALTER TABLE public.request_logs_hot ADD COLUMN IF NOT EXISTS node_switch_count INTEGER DEFAULT 0;
ALTER TABLE public.request_logs_hot ADD COLUMN IF NOT EXISTS raw_model_name TEXT;
ALTER TABLE public.request_logs_hot ADD COLUMN IF NOT EXISTS system_fingerprint TEXT;
ALTER TABLE public.request_logs_hot ADD COLUMN IF NOT EXISTS timeout_mode VARCHAR(50);

-- ─── L. 回填默认值（如果 hot 表已有数据）───
-- hot 表可能有历史 NULL 数据，回填默认避免读取时 null 处理麻烦
UPDATE public.request_logs_hot
   SET is_continuation = FALSE
 WHERE is_continuation IS NULL;
UPDATE public.request_logs_hot
   SET keepalive_sent_count = 0
 WHERE keepalive_sent_count IS NULL;
UPDATE public.request_logs_hot
   SET node_switch_count = 0
 WHERE node_switch_count IS NULL;

-- ─── M. View 防御性检查（rule 49 §9.2）───
-- 603 改动了 hot 和 parent 的列结构；view 用 explicit column list 不会受影响，
-- 但需要 explicit 验证（避免 silent schema-vs-view drift）
DO $$
BEGIN
    -- 检查 view 1 (with_current_month) 仍能查询
    PERFORM 1 FROM public.request_logs_with_current_month LIMIT 1;
    -- 检查 view 2 (without_customer_id) UNION 仍对齐
    PERFORM 1 FROM public.request_logs_with_current_month_without_customer_id LIMIT 1;
    RAISE NOTICE 'rule 49 §9.2: both views queryable, no schema-vs-view drift';
EXCEPTION WHEN OTHERS THEN
    RAISE EXCEPTION 'rule 49 §9.2 violation: views broken after schema change: %', SQLERRM;
END $$;

COMMIT;

-- ===========================================================================
-- Post-deploy verification (rule 38 §6.5):
--   ssh -p 25022 root@115.29.212.252 docker exec pg-252-pg17 psql -U llm_gateway -d llm_gateway
--   SELECT column_name FROM information_schema.columns
--    WHERE table_schema='public' AND table_name='request_logs_hot'
--      AND column_name NOT IN (SELECT column_name FROM information_schema.columns
--                               WHERE table_schema='public' AND table_name='request_logs')
--      AND column_name NOT IN ('caller_id','session_correlation_id','status_code');
--   expected: 0 rows
-- ===========================================================================
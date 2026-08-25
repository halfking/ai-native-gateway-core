-- ===========================================================================
-- File:          sql/migrations/startup/603_repair_request_logs_schema_consistency.down.sql
-- Migration:     603 (DOWN)
-- Purpose:       回滚 603 修复（恢复 request_logs.outbound_body + 删除 hot 表新加列）
--
-- Status:        idempotent (全部 IF EXISTS / IF NOT EXISTS + 单事务包裹)
--
-- ⚠️  WARNING: 此 down 脚本**故意**与 603 up 不对称：
--   - 603 修复了"parent.request_logs.outbound_body 残留"（573 漏掉）
--   - down 会重新添加该列以完全撤销 603 的所有变更
--   - 实际数据影响：0 行（parent.request_logs 是空表）
--   - 业务代码影响：outbound_body 是 573/602 主动剥离的字段，重新添加不会
--     引入功能（业务代码不读 parent.outbound_body）但会让 schema 回到 573 之前
--
-- Safety:
--   - 单事务包裹：要么全成功要么全失败
--   - 加列 jsonb fast path：不重写数据
-- ===========================================================================

\set ON_ERROR_STOP on
BEGIN;

-- 1. DROP hot 表 603 新加的 10 列（按依赖顺序倒序）
ALTER TABLE public.request_logs_hot DROP COLUMN IF EXISTS timeout_mode;
ALTER TABLE public.request_logs_hot DROP COLUMN IF EXISTS system_fingerprint;
ALTER TABLE public.request_logs_hot DROP COLUMN IF EXISTS raw_model_name;
ALTER TABLE public.request_logs_hot DROP COLUMN IF EXISTS node_switch_count;
ALTER TABLE public.request_logs_hot DROP COLUMN IF EXISTS keepalive_sent_count;
ALTER TABLE public.request_logs_hot DROP COLUMN IF EXISTS is_continuation;
ALTER TABLE public.request_logs_hot DROP COLUMN IF EXISTS effective_timeout_seconds;
ALTER TABLE public.request_logs_hot DROP COLUMN IF EXISTS continuation_keywords;
ALTER TABLE public.request_logs_hot DROP COLUMN IF EXISTS context_size_tokens;
ALTER TABLE public.request_logs_hot DROP COLUMN IF EXISTS cached_response_id;

-- 2. 恢复 parent.request_logs.outbound_body（573 漏掉的，603 清理掉了）
ALTER TABLE public.request_logs ADD COLUMN IF NOT EXISTS outbound_body JSONB;

-- 3. View 防御性检查（rule 49 §9.2）
DO $$
BEGIN
    PERFORM 1 FROM public.request_logs_with_current_month LIMIT 1;
    PERFORM 1 FROM public.request_logs_with_current_month_without_customer_id LIMIT 1;
    RAISE NOTICE 'rule 49 §9.2: both views queryable after 603 down';
EXCEPTION WHEN OTHERS THEN
    RAISE EXCEPTION 'rule 49 §9.2 violation post-down: %', SQLERRM;
END $$;

COMMIT;

-- ===========================================================================
-- Post-down verification (rule 38 §6.5):
--   ssh -p 25022 root@115.29.212.252 docker exec pg-252-pg17 psql -U llm_gateway -d llm_gateway
--   SELECT column_name FROM information_schema.columns
--    WHERE table_schema='public' AND table_name='request_logs_hot'
--      AND column_name IN ('cached_response_id','context_size_tokens','continuation_keywords',
--                          'effective_timeout_seconds','is_continuation','keepalive_sent_count',
--                          'node_switch_count','raw_model_name','system_fingerprint','timeout_mode');
--   expected: 0 rows (603 加的列全被 DROP)
--
--   SELECT column_name FROM information_schema.columns
--    WHERE table_schema='public' AND table_name='request_logs' AND column_name='outbound_body';
--   expected: 1 row (outbound_body 已恢复)
-- ===========================================================================
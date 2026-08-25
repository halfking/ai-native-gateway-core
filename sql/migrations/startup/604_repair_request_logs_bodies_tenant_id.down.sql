-- ===========================================================================
-- File:          sql/migrations/startup/604_repair_request_logs_bodies_tenant_id.down.sql
-- Migration:     604 (DOWN)
-- Purpose:       回滚 604 修复（恢复 request_logs_bodies.tenant_id）
--
-- Status:        idempotent (IF NOT EXISTS + 单事务)
--
-- ⚠️  WARNING: 回滚会重新添加 parent.request_logs_bodies.tenant_id
--     实际数据影响：0 行（601 已 DROP parent hot 侧的 tenant_id 数据，
--     但 parent 表 tenant_id 都是 NULL，加列默认 NULL）
-- ===========================================================================

\set ON_ERROR_STOP on
BEGIN;

ALTER TABLE public.request_logs_bodies ADD COLUMN IF NOT EXISTS tenant_id TEXT;

-- View 防御性检查（rule 49 §9.2）
DO $$
BEGIN
    PERFORM 1 FROM public.request_logs_bodies_with_current_month LIMIT 1;
    RAISE NOTICE 'rule 49 §9.2: bodies view queryable after 604 down';
EXCEPTION WHEN OTHERS THEN
    RAISE EXCEPTION 'rule 49 §9.2 violation post-down: %', SQLERRM;
END $$;

COMMIT;

-- Post-down verification:
--   ssh -p 25022 root@115.29.212.252 docker exec pg-252-pg17 psql -U llm_gateway -d llm_gateway
--   SELECT column_name FROM information_schema.columns
--    WHERE table_schema='public' AND table_name='request_logs_bodies' AND column_name='tenant_id';
--   expected: 1 row

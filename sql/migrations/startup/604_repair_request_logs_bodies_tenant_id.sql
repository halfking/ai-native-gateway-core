-- ===========================================================================
-- File:          sql/migrations/startup/604_repair_request_logs_bodies_tenant_id.sql
-- Migration:     604
-- Database:      llm_gateway
-- Purpose:       DROP request_logs_bodies 的 tenant_id 残留列
--
-- Status:        active
-- Idempotent:    YES (IF EXISTS + 单事务)
-- Dependencies:  601_request_logs_bodies_drop_metadata (已 DROP hot 的 tenant_id)
--
-- Background:
--   Migration 601 (2026-08-25) DROP 了 request_logs_bodies_hot.tenant_id，
--   但**遗漏了 parent 表 request_logs_bodies**。两边应保持一致：
--   bodies 表只存 body 内容（request_body / outbound_body / response_body），
--   tenant_id 应通过 request_id JOIN request_logs_hot 解析（rule 22 §9.1 原则）。
--
-- View considerations (rule 49 §9.2):
--   - 604 只 DROP parent.request_logs_bodies.tenant_id
--   - view request_logs_bodies_with_current_month 不引用 tenant_id
--     (验证 2026-08-25 19:40 CST: tenant_id_refs=0)
--   - 但仍 explicit 验证 view 可查
--
-- Affected Rows: 0 (实际 parent.request_logs_bodies 12721 行 tenant_id 全为 NULL)
-- Breaking Change: NO
-- Rollback Script: 604_repair_request_logs_bodies_tenant_id.down.sql
--
-- Date: 2026-08-25
-- Author: ZCode
-- Refs: docs/audits/2026-08-25-252-pg-schema-consistency-report.md
-- ===========================================================================

\set ON_ERROR_STOP on
BEGIN;

-- DROP parent 表的 tenant_id 残留列（与 hot 表保持一致）
ALTER TABLE public.request_logs_bodies DROP COLUMN IF EXISTS tenant_id;

-- View 防御性检查（rule 49 §9.2）
DO $$
BEGIN
    PERFORM 1 FROM public.request_logs_bodies_with_current_month LIMIT 1;
    RAISE NOTICE 'rule 49 §9.2: bodies view queryable';
EXCEPTION WHEN OTHERS THEN
    RAISE EXCEPTION 'rule 49 §9.2 violation: bodies view broken after DROP: %', SQLERRM;
END $$;

COMMIT;

-- Post-deploy verification:
--   ssh -p 25022 root@115.29.212.252 docker exec pg-252-pg17 psql -U llm_gateway -d llm_gateway
--   SELECT column_name FROM information_schema.columns
--    WHERE table_schema='public' AND table_name='request_logs_bodies';
--   expected: 5 columns (request_id, ts, request_body, outbound_body, response_body)
--             matching request_logs_bodies_hot

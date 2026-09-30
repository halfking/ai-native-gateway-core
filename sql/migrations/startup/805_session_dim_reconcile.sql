-- ===========================================================================
-- File:          sql/migrations/startup/805_session_dim_reconcile.sql
-- Migration:     805
-- Database:      llm_gateway
-- Purpose:       canonical 链收编 session_dim 映射表（683 的前置缺口）
--
-- Status:        active
-- Idempotent:    YES (CREATE TABLE IF NOT EXISTS / ADD COLUMN IF NOT EXISTS /
--                CREATE INDEX IF NOT EXISTS / DROP POLICY IF EXISTS)
-- Dependencies:  310_session_summaries.sql（session_summaries 家族语义）
--                683_session_dim_ownership_columns.sql（同族列对账，序后于本迁移）
--
-- Background:
--   session_dim 由 350 引入（gw_session_id ↔ session_key ↔ task_id 生命周期
--   映射，sessionv2mirror 写入目标），但 350 与 358 均低于 installer 的 478
--   下限且不可整文件注册：350 会以 2026-07 旧函数体重写 update_session_summary()
--   （683 头注明示的 clobber guard 禁区），358 同理。baseline 01-schema 对
--   session_dim 零引用——canonical 新装链在 683 ALTER session_dim 时 42P01
--   中断（2026-10-01 fresh-install e2e 实证）。
--
--   本迁移按生产真库形态（2026-10-01 实测 17 列 + pkey + 6 索引 + RLS）
--   建表：350 的基列 + project_id + 358/683 的 ownership 列一次给全，
--   683 随后的 ADD COLUMN IF NOT EXISTS 退化为 no-op、索引段照常生效。
--   触发器/函数链（563/572）与 350 的两个分析视图由 baseline + 既有序列
--   负责，本迁移不触碰。
-- ===========================================================================

BEGIN;

CREATE TABLE IF NOT EXISTS public.session_dim (
    gw_session_id      character varying NOT NULL,
    session_key        character varying NOT NULL,
    tenant_id          character varying NOT NULL,
    task_id            character varying,
    status             character varying NOT NULL,
    first_request_at   timestamp with time zone,
    last_active_at     timestamp with time zone,
    closed_at          timestamp with time zone,
    created_at         timestamp with time zone NOT NULL,
    project_id         text,
    api_key_id         bigint,
    application_id     bigint,
    application_code   character varying,
    owner_user         character varying,
    api_key_owner_user character varying,
    end_user_id        character varying,
    client_id          character varying,
    CONSTRAINT session_dim_pkey PRIMARY KEY (gw_session_id)
);

CREATE INDEX IF NOT EXISTS idx_session_dim_tenant
    ON public.session_dim (tenant_id, last_active_at DESC);
CREATE INDEX IF NOT EXISTS idx_session_dim_task
    ON public.session_dim (tenant_id, task_id) WHERE task_id IS NOT NULL;

ALTER TABLE public.session_dim ENABLE ROW LEVEL SECURITY;

DO $$
BEGIN
    DROP POLICY IF EXISTS session_dim_tenant_isolation ON public.session_dim;
    CREATE POLICY session_dim_tenant_isolation ON public.session_dim
        USING (((tenant_id)::text = current_setting('app.current_tenant'::text, true)));
    DROP POLICY IF EXISTS session_dim_super_admin_bypass ON public.session_dim;
    CREATE POLICY session_dim_super_admin_bypass ON public.session_dim
        USING (((current_setting('app.current_role'::text, true) = 'super_admin'::text)
            OR (current_setting('app.bypass_rls'::text, true) = 'true'::text)));
END $$;

COMMIT;

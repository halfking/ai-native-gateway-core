-- ===========================================================================
-- File:          sql/migrations/startup/998_session_summaries_task_association.down.sql
-- Migration:     998 (rollback)
-- Database:      llm_gateway
-- Purpose:       回滚 session_summaries 表的任务关联扩展
--
-- Date: 2026-11-21
-- Author: Kiro AI
-- ===========================================================================

\set ON_ERROR_STOP on
BEGIN;
SET lock_timeout = '5s';
SET statement_timeout = '10min';
-- 删除索引
DROP INDEX IF EXISTS public.idx_session_summaries_task;
DROP INDEX IF EXISTS public.idx_session_summaries_canonical;
DROP INDEX IF EXISTS public.idx_session_summaries_dedup_candidates;
DROP INDEX IF EXISTS public.idx_session_summaries_layered_pending;

-- 删除列
ALTER TABLE public.session_summaries
    DROP COLUMN IF EXISTS task_id,
    DROP COLUMN IF EXISTS similar_session_keys,
    DROP COLUMN IF EXISTS is_duplicate,
    DROP COLUMN IF EXISTS canonical_session_key,
    DROP COLUMN IF EXISTS has_layered_summary;

-- 台账回退（与 up 的自登记对偶 —— 995/996 同款；down 只退自己引入的行）
DELETE FROM public.schema_migrations WHERE version = '998';

COMMIT;

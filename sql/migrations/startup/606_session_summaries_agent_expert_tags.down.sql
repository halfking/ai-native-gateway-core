-- ===========================================================================
-- Rollback: 606_session_summaries_agent_expert_tags.down.sql
-- Purpose:  回退 migration 606 —— DROP session_summaries 的
--           agent_type / expert_type / tags 三列
-- Idempotent: YES (DROP COLUMN IF EXISTS + 单事务)
-- ===========================================================================

\set ON_ERROR_STOP on
BEGIN;

ALTER TABLE public.session_summaries
    DROP COLUMN IF EXISTS agent_type,
    DROP COLUMN IF EXISTS expert_type,
    DROP COLUMN IF EXISTS tags;

COMMIT;

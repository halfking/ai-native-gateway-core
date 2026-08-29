-- ===========================================================================
-- File:          sql/migrations/startup/617_session_turns_unified_view.down.sql
-- Database:      llm_gateway
-- Purpose:       回滚 session_turns_unified 统一视图
--
-- Related:       617_session_turns_unified_view.sql
-- ===========================================================================

\set ON_ERROR_STOP on

-- 删除统一视图
DROP VIEW IF EXISTS session_turns_unified;

RAISE NOTICE '✅ Migration 617 rolled back: session_turns_unified view removed';

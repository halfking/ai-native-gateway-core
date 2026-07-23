-- ============================================================================
-- Migration 346: system_probe_run_tokens
-- Purpose:  Persist token usage for live system-monitor totals.
-- Object Type: TABLE ALTER
-- Rollback: sql/migrations/domain/346_system_probe_run_tokens.down.sql
-- ============================================================================
\set ON_ERROR_STOP on
BEGIN;
ALTER TABLE system_probe_runs ADD COLUMN IF NOT EXISTS total_tokens BIGINT NOT NULL DEFAULT 0;
COMMIT;

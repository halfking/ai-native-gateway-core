-- Migration 359: Fix handoff schema consistency
--
-- Problem: SQLSTATE 42P08 "inconsistent types deduced for parameter $2"
-- Root cause: Missing columns in session_summaries or handoff_logs
-- causing parameter position mismatch in SQL queries.
--
-- This migration ensures all required columns exist.

BEGIN;

-- 1. Ensure session_summaries has all required handoff tracking columns
ALTER TABLE session_summaries
    ADD COLUMN IF NOT EXISTS handoff_count INT DEFAULT 0;

ALTER TABLE session_summaries
    ADD COLUMN IF NOT EXISTS last_handoff_at TIMESTAMPTZ;

ALTER TABLE session_summaries
    ADD COLUMN IF NOT EXISTS tokens_at_trigger BIGINT DEFAULT 0;

ALTER TABLE session_summaries
    ADD COLUMN IF NOT EXISTS messages_at_trigger INT DEFAULT 0;

ALTER TABLE session_summaries
    ADD COLUMN IF NOT EXISTS last_trigger_reason VARCHAR(64);

ALTER TABLE session_summaries
    ADD COLUMN IF NOT EXISTS last_trigger_at TIMESTAMPTZ;

-- 2. Ensure handoff_logs has all required columns (from 354 + 356)
ALTER TABLE handoff_logs
    ADD COLUMN IF NOT EXISTS summary_text TEXT;

ALTER TABLE handoff_logs
    ADD COLUMN IF NOT EXISTS summary_engine VARCHAR(32);

ALTER TABLE handoff_logs
    ADD COLUMN IF NOT EXISTS trigger_mode VARCHAR(32);

ALTER TABLE handoff_logs
    ADD COLUMN IF NOT EXISTS tokens_in_session INT;

ALTER TABLE handoff_logs
    ADD COLUMN IF NOT EXISTS messages_in_session INT;

ALTER TABLE handoff_logs
    ADD COLUMN IF NOT EXISTS skill_name VARCHAR(64);

ALTER TABLE handoff_logs
    ADD COLUMN IF NOT EXISTS duration_ms INT;

-- 3. Add indexes if they don't exist
CREATE INDEX IF NOT EXISTS idx_session_summaries_handoff
    ON session_summaries(last_handoff_at)
    WHERE handoff_count > 0;

CREATE INDEX IF NOT EXISTS idx_handoff_logs_trigger_mode
    ON handoff_logs(trigger_reason, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_handoff_logs_tenant_created
    ON handoff_logs(tenant_id, created_at DESC);

COMMIT;
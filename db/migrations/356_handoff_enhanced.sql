-- Migration 356: handoff enhanced columns
--
-- 2026-07-09: As part of restoring the auto-handoff hook (domains/hooks/handoff,
-- previously parked at _to-be-deprecated/hooks-handoff-20260706/) we need a few
-- extra columns so the operator/admin UI can:
--   1. Display the actual summary text that was generated (audit + UX)
--   2. Show which summary engine produced it (llm / rule / hybrid)
--   3. Show the trigger mode (auto / manual / hybrid) for forensic analysis
--   4. Pre-aggregate cumulative tokens on session_summaries so the
--      "auto_control.enabled" hook does not need to round-trip a second
--      query on the hot path. We deliberately call the new column
--      total_tokens_used (different from the existing total_tokens
--      generated column) so we don't disturb the existing aggregates
--      used by dashboards / analytics.

BEGIN;

-- 1. Extend handoff_logs with summary payload + provenance metadata.
ALTER TABLE handoff_logs
    ADD COLUMN IF NOT EXISTS summary_text      TEXT,
    ADD COLUMN IF NOT EXISTS summary_engine    VARCHAR(32),
    ADD COLUMN IF NOT EXISTS trigger_mode      VARCHAR(32),
    ADD COLUMN IF NOT EXISTS tokens_in_session INT,
    ADD COLUMN IF NOT EXISTS messages_in_session INT,
    ADD COLUMN IF NOT EXISTS skill_name        VARCHAR(64),
    ADD COLUMN IF NOT EXISTS duration_ms       INT;

-- Cheap sanity indexes — useful when the operator filters by trigger
-- reason in the admin UI / dashboards.
CREATE INDEX IF NOT EXISTS idx_handoff_logs_trigger_mode
    ON handoff_logs(trigger_reason, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_handoff_logs_tenant_created
    ON handoff_logs(tenant_id, created_at DESC);

-- 2. Track cumulative tokens on session_summaries so the hook can decide
--    "absolute threshold reached" without a SUM(*) over request_logs.
--
--    Naming: `tokens_at_trigger` (NOT `total_tokens_used`) to avoid
--    colliding with the existing total_tokens generated column.
ALTER TABLE session_summaries
    ADD COLUMN IF NOT EXISTS tokens_at_trigger BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS messages_at_trigger INT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS last_trigger_reason VARCHAR(64),
    ADD COLUMN IF NOT EXISTS last_trigger_at TIMESTAMPTZ;

COMMIT;
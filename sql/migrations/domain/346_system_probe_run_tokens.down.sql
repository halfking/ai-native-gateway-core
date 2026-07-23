-- Rollback migration 346: remove persisted system-monitor token usage.
\set ON_ERROR_STOP on
BEGIN;
ALTER TABLE system_probe_runs DROP COLUMN IF EXISTS total_tokens;
COMMIT;

-- 458 migration down: remove canonical_model + client perception columns
-- 2026-07-27: companion to 458_request_logs_canonical_model.sql
--
-- DROP COLUMN IF EXISTS removes the new columns. PostgreSQL propagates
-- the drop to all existing partitions of the partitioned parent table
-- (request_logs_2026_07, request_logs_2026_08, etc.) automatically.
--
-- This script is idempotent (DROP COLUMN IF EXISTS).

BEGIN;

ALTER TABLE request_logs
    DROP COLUMN IF EXISTS canonical_model;

ALTER TABLE request_logs_hot
    DROP COLUMN IF EXISTS canonical_model;

-- 2026-07-27 (audit fix): the up migration also adds the four client-perception
-- columns to request_logs_hot (agent_name / agent_type / client_protocol /
-- virtual_client_id). The up migration's own comment claimed "the existing
-- 458 down.sql already DROPs them" — that was false; this is the fix that
-- restores the up/down invariant (down removes exactly what up adds).
-- IF EXISTS keeps this safe even if migration 459 (which re-adds three of
-- them to the hot table) runs afterwards in a forward roll.
-- Migration 459 is rolled back before 458, so by the time this runs the
-- columns exist solely because 458 added them.
ALTER TABLE request_logs_hot
    DROP COLUMN IF EXISTS agent_name,
    DROP COLUMN IF EXISTS agent_type,
    DROP COLUMN IF EXISTS client_protocol,
    DROP COLUMN IF EXISTS virtual_client_id;

-- Index cleanup (the indexes were created by the up migration).
DROP INDEX IF EXISTS idx_request_logs_hot_canonical_model_ts;
DROP INDEX IF EXISTS idx_request_logs_canonical_model_ts;

COMMIT;

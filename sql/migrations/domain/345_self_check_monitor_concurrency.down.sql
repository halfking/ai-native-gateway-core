-- ============================================================================
-- Migration 345 down: drop self_check_settings.monitor_concurrency
-- ===========================================================================

\set ON_ERROR_STOP on

BEGIN;

ALTER TABLE self_check_settings
    DROP COLUMN IF EXISTS monitor_concurrency;

COMMIT;
-- Rollback for 442_session_summaries_missing_columns.sql.
-- Run only after confirming these columns were introduced by migration 442.

BEGIN;

ALTER TABLE IF EXISTS session_summaries
    DROP COLUMN IF EXISTS health_score,
    DROP COLUMN IF EXISTS health_grade,
    DROP COLUMN IF EXISTS range,
    DROP COLUMN IF EXISTS last_health_at;

COMMIT;

-- Rollback for 441_session_state_missing_columns.sql.
-- Run only after confirming these columns were introduced by migration 441.

BEGIN;

ALTER TABLE IF EXISTS session_state
    DROP COLUMN IF EXISTS input_cost_usd,
    DROP COLUMN IF EXISTS output_cost_usd,
    DROP COLUMN IF EXISTS health_score,
    DROP COLUMN IF EXISTS health_grade,
    DROP COLUMN IF EXISTS range,
    DROP COLUMN IF EXISTS last_health_at;

COMMIT;

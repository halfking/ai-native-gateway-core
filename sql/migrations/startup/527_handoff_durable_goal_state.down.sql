BEGIN;

DROP INDEX IF EXISTS idx_handoff_pending_restore;
ALTER TABLE handoff_pending_confirmations
    DROP CONSTRAINT IF EXISTS handoff_pending_confirmations_status_check;
ALTER TABLE handoff_pending_confirmations
    ADD CONSTRAINT handoff_pending_confirmations_status_check
    CHECK (status IN ('pending', 'confirmed', 'expired'));
ALTER TABLE handoff_pending_confirmations
    DROP COLUMN IF EXISTS goal_state,
    DROP COLUMN IF EXISTS goal_state_version,
    DROP COLUMN IF EXISTS restore_status,
    DROP COLUMN IF EXISTS restore_error,
    DROP COLUMN IF EXISTS restore_attempted_at,
    DROP COLUMN IF EXISTS restored_at;

COMMIT;

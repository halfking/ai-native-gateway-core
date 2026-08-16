BEGIN;

DROP INDEX IF EXISTS idx_handoff_pending_restore;
ALTER TABLE handoff_pending_confirmations
    DROP CONSTRAINT IF EXISTS handoff_pending_confirmations_status_check;
-- Roll non-{pending,confirmed,expired} rows back before shrinking the column.
UPDATE handoff_pending_confirmations
SET status = 'confirmed'
WHERE status NOT IN ('pending', 'confirmed', 'expired');
ALTER TABLE handoff_pending_confirmations
    ALTER COLUMN status TYPE VARCHAR(16);
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

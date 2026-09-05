BEGIN;

ALTER TABLE handoff_pending_confirmations
    DROP CONSTRAINT IF EXISTS handoff_pending_confirmations_status_check;

ALTER TABLE handoff_pending_confirmations
    ALTER COLUMN status TYPE VARCHAR(32),
    ADD COLUMN IF NOT EXISTS goal_state JSONB,
    ADD COLUMN IF NOT EXISTS goal_state_version INTEGER,
    ADD COLUMN IF NOT EXISTS restore_status VARCHAR(32),
    ADD COLUMN IF NOT EXISTS restore_error VARCHAR(512),
    ADD COLUMN IF NOT EXISTS restore_attempted_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS restored_at TIMESTAMPTZ;

ALTER TABLE handoff_pending_confirmations
    ADD CONSTRAINT handoff_pending_confirmations_status_check
    CHECK (status IN ('pending', 'confirmed', 'accounting_confirmed', 'restored', 'manual_required', 'expired'));

UPDATE handoff_pending_confirmations
SET status = 'accounting_confirmed',
    restore_status = COALESCE(restore_status, 'accounting_confirmed')
WHERE status = 'confirmed';

CREATE INDEX IF NOT EXISTS idx_handoff_pending_restore
    ON handoff_pending_confirmations (tenant_id, status, proposal_created_at)
    WHERE status IN ('accounting_confirmed', 'manual_required');

COMMIT;

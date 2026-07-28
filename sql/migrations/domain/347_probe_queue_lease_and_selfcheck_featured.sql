-- Migration 347: make durable probe leases owner-safe and allow featured
-- credential self-check strategy records.
BEGIN;

ALTER TABLE credential_probe_queue
    ADD COLUMN IF NOT EXISTS lease_token UUID;

CREATE INDEX IF NOT EXISTS idx_credential_probe_queue_running_lease_token
    ON credential_probe_queue (id, lease_token)
    WHERE status = 'running';

ALTER TABLE self_check_runs
    DROP CONSTRAINT IF EXISTS self_check_runs_selection_strategy_check;
ALTER TABLE self_check_runs
    ADD CONSTRAINT self_check_runs_selection_strategy_check CHECK (
        selection_strategy IS NULL
        OR selection_strategy = 'featured'
        OR selection_strategy = 'most_used'
        OR selection_strategy LIKE 'fallback_%'
        OR selection_strategy = 'random'
    );

COMMENT ON COLUMN self_check_runs.selection_strategy IS
'347: featured | most_used | fallback_<n> | random — how credential_selfcheck selected its model.';

COMMIT;

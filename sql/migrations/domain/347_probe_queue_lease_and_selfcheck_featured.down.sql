-- Rollback migration 347.
BEGIN;

DROP INDEX IF EXISTS idx_credential_probe_queue_running_lease_token;
ALTER TABLE credential_probe_queue
    DROP COLUMN IF EXISTS lease_token;

ALTER TABLE self_check_runs
    DROP CONSTRAINT IF EXISTS self_check_runs_selection_strategy_check;
ALTER TABLE self_check_runs
    ADD CONSTRAINT self_check_runs_selection_strategy_check CHECK (
        selection_strategy IS NULL
        OR selection_strategy = 'most_used'
        OR selection_strategy LIKE 'fallback_%'
        OR selection_strategy = 'random'
    );

COMMIT;

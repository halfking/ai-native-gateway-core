-- Migration 341 (down): revert origin_stage/origin_actor, node_probe tables,
-- system_health_status, credential_most_used_model, and the self_check
-- status CHECK extension.
BEGIN;

ALTER TABLE self_check_runs
    DROP CONSTRAINT IF EXISTS self_check_runs_selection_strategy_check;
ALTER TABLE self_check_runs
    DROP COLUMN IF EXISTS attempted_models,
    DROP COLUMN IF EXISTS selection_strategy;
ALTER TABLE self_check_runs
    DROP CONSTRAINT IF EXISTS self_check_runs_status_check;
ALTER TABLE self_check_runs
    ADD CONSTRAINT self_check_runs_status_check CHECK (
        status IN ('running','success','partial','failed')
    );

DROP FUNCTION IF EXISTS credential_most_used_model(bigint, integer);
DROP FUNCTION IF EXISTS system_health_status(integer);

DROP INDEX IF EXISTS idx_node_probe_state_due;
DROP TABLE IF EXISTS node_probe_state;

DROP INDEX IF EXISTS idx_node_probe_runs_started;
DROP INDEX IF EXISTS idx_node_probe_runs_cred_model;
DROP TABLE IF EXISTS node_probe_runs;

DROP INDEX IF EXISTS idx_request_logs_hot_origin_stage_ts;
ALTER TABLE request_logs
    DROP CONSTRAINT IF EXISTS request_logs_origin_stage_check;
ALTER TABLE request_logs
    DROP COLUMN IF EXISTS origin_actor,
    DROP COLUMN IF EXISTS origin_stage;
ALTER TABLE request_logs_hot
    DROP COLUMN IF EXISTS origin_actor,
    DROP COLUMN IF EXISTS origin_stage;

COMMIT;

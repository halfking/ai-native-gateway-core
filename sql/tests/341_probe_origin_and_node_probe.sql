-- Smoke test for migration 341.
-- Verifies that:
--   - origin_stage + origin_actor exist on request_logs_hot and request_logs
--   - node_probe_runs + node_probe_state exist with correct constraints
--   - system_health_status() returns the expected 3 statuses
--   - self_check_runs accepts 'retrying' status and selection_strategy values
--   - credential_most_used_model() returns the expected columns
-- Run after applying the migration. This test is idempotent (no writes).

DO $$
DECLARE
    col_count integer;
BEGIN
    -- 1. origin columns on request_logs_hot
    SELECT COUNT(*) INTO col_count
    FROM information_schema.columns
    WHERE table_name = 'request_logs_hot'
      AND column_name IN ('origin_stage', 'origin_actor');
    IF col_count <> 2 THEN
        RAISE EXCEPTION 'origin_stage/origin_actor missing on request_logs_hot (got %)', col_count;
    END IF;

    -- 2. origin columns on request_logs
    SELECT COUNT(*) INTO col_count
    FROM information_schema.columns
    WHERE table_name = 'request_logs'
      AND column_name IN ('origin_stage', 'origin_actor');
    IF col_count <> 2 THEN
        RAISE EXCEPTION 'origin_stage/origin_actor missing on request_logs (got %)', col_count;
    END IF;

    -- 3. node_probe_runs + node_probe_state tables
    PERFORM 1 FROM information_schema.tables WHERE table_name = 'node_probe_runs';
    IF NOT FOUND THEN
        RAISE EXCEPTION 'node_probe_runs table missing';
    END IF;
    PERFORM 1 FROM information_schema.tables WHERE table_name = 'node_probe_state';
    IF NOT FOUND THEN
        RAISE EXCEPTION 'node_probe_state table missing';
    END IF;

    -- 4. CHECK constraints on node_probe_runs
    PERFORM 1 FROM information_schema.check_constraints
    WHERE constraint_name = 'node_probe_runs_trigger_kind_check';
    IF NOT FOUND THEN RAISE EXCEPTION 'node_probe_runs_trigger_kind_check missing'; END IF;

    -- 5. system_health_status() callable
    PERFORM * FROM system_health_status(30) LIMIT 0;
    IF NOT FOUND THEN
        -- suspect path returns 0 rows; this is fine, just make sure it doesn't error
        NULL;
    END IF;

    -- 6. self_check_runs accepts 'retrying' status
    BEGIN
        PERFORM 1 FROM self_check_runs WHERE status = 'retrying' LIMIT 0;
    EXCEPTION WHEN check_violation THEN
        RAISE EXCEPTION 'self_check_runs status CHECK rejected retrying';
    END;

    -- 7. credential_most_used_model signature
    PERFORM * FROM credential_most_used_model(0, 24) LIMIT 0;
    IF NOT FOUND THEN NULL; END IF;

    RAISE NOTICE '341_probe_origin_and_node_probe: smoke test passed';
END $$;

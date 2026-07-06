-- Migration 351: request_logs_bodies hot table independence
--
-- Background:
--   request_logs_bodies stores the request/response bodies for LLM requests.
--   It follows the same hot table architecture as request_logs, usage_ledger, etc.
--
--   Current state:
--     - request_logs_bodies (partitioned by RANGE(ts))
--     - request_logs_bodies_2026_06 (partition, columnar)
--     - No hot table exists yet
--
--   Problem:
--     - Without a hot table, all writes go to the default partition
--     - The partition_manager cannot manage the lifecycle properly
--     - No promote function exists to migrate cold data
--
-- Solution:
--   1. Create request_logs_bodies_hot as an independent heap table
--   2. Create indexes for performance
--   3. Create promote function for cold data migration
--   4. Create/update the with_current_month view
--
-- Author: ACC team (2026-07-06)

BEGIN;

-- ============================================================
-- 1. Create hot table
-- ============================================================

CREATE TABLE IF NOT EXISTS request_logs_bodies_hot (
    -- Copy structure from request_logs_bodies
    LIKE request_logs_bodies INCLUDING DEFAULTS INCLUDING CONSTRAINTS
);

COMMENT ON TABLE request_logs_bodies_hot IS
'Hot data table (0-7 days) for request_logs_bodies. 
Completely independent from partitioned table.
Data older than 7 days is migrated to request_logs_bodies monthly partitions 
by promote_request_logs_bodies_hot_to_partition().
Created by migration 351 (2026-07-06).';

-- ============================================================
-- 2. Create indexes
-- ============================================================

-- 2.1 Timestamp index (most common)
CREATE INDEX IF NOT EXISTS idx_request_logs_bodies_hot_ts 
  ON request_logs_bodies_hot (ts DESC);

-- 2.2 Unique constraint (supports UPSERT)
CREATE UNIQUE INDEX IF NOT EXISTS idx_request_logs_bodies_hot_request_id_ts_unique
  ON request_logs_bodies_hot (request_id, ts);

-- 2.3 Request ID index (point queries)
CREATE INDEX IF NOT EXISTS idx_request_logs_bodies_hot_request_id
  ON request_logs_bodies_hot (request_id);

DO $$ BEGIN RAISE NOTICE 'Created indexes on request_logs_bodies_hot'; END $$;

-- ============================================================
-- 3. Create promote function
-- ============================================================

CREATE OR REPLACE FUNCTION promote_request_logs_bodies_hot_to_partition(
    p_retention interval DEFAULT '7 days',
    p_batch_size int DEFAULT 5000
) RETURNS bigint
LANGUAGE plpgsql
AS $$
DECLARE
    v_moved bigint := 0;
BEGIN
    -- Move rows older than retention to the partitioned table
    -- PostgreSQL will automatically route them to the correct monthly partition
    WITH batch AS (
        SELECT request_id, ts, request_body, outbound_body, response_body
        FROM request_logs_bodies_hot
        WHERE ts < now() - p_retention
        ORDER BY ts
        LIMIT p_batch_size
    ),
    deleted AS (
        DELETE FROM request_logs_bodies_hot
        WHERE (request_id, ts) IN (SELECT request_id, ts FROM batch)
        RETURNING *
    )
    INSERT INTO request_logs_bodies (request_id, ts, request_body, outbound_body, response_body)
    SELECT request_id, ts, request_body, outbound_body, response_body FROM deleted
    ON CONFLICT (request_id, ts) DO NOTHING;
    
    GET DIAGNOSTICS v_moved = ROW_COUNT;
    RETURN v_moved;
END;
$$;

COMMENT ON FUNCTION promote_request_logs_bodies_hot_to_partition(interval, int) IS
'Migrate cold rows from request_logs_bodies_hot to monthly partitions.
Returns the number of rows moved. Call in a loop until it returns 0.
Created by migration 351 (2026-07-06).';

-- ============================================================
-- 4. Create or replace the with_current_month view
-- ============================================================

DROP VIEW IF EXISTS request_logs_bodies_with_current_month CASCADE;

CREATE VIEW request_logs_bodies_with_current_month AS
SELECT * FROM request_logs_bodies_hot
UNION ALL
SELECT * FROM request_logs_bodies;

COMMENT ON VIEW request_logs_bodies_with_current_month IS
'Combines hot data (0-7 days) with all partitioned data (monthly partitions).
Use this view for queries that need to see recent data plus historical data.
Created by migration 351 (2026-07-06).';

-- ============================================================
-- 5. Verify migration
-- ============================================================

DO $$
DECLARE
    hot_count bigint;
    view_count bigint;
    func_exists boolean;
BEGIN
    -- Check hot table
    SELECT count(*) INTO hot_count FROM request_logs_bodies_hot;
    RAISE NOTICE 'request_logs_bodies_hot contains % rows', hot_count;
    
    -- Check view
    SELECT count(*) INTO view_count FROM request_logs_bodies_with_current_month;
    RAISE NOTICE 'request_logs_bodies_with_current_month view works (% rows)', view_count;
    
    -- Check promote function
    SELECT EXISTS (
        SELECT 1 FROM pg_proc 
        WHERE proname = 'promote_request_logs_bodies_hot_to_partition'
    ) INTO func_exists;
    
    IF func_exists THEN
        RAISE NOTICE 'Migration 351 verification PASSED';
        RAISE NOTICE '  - hot table: % rows (storage: heap)', hot_count;
        RAISE NOTICE '  - view: exists';
        RAISE NOTICE '  - promote function: exists';
    ELSE
        RAISE EXCEPTION 'Migration 351 verification FAILED: promote function not created';
    END IF;
END $$;

COMMIT;

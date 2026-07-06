-- Migration 354: credential_model_index hot table independence
--
-- Background:
--   credential_model_index stores the health index for credential-model pairs.
--   It's used for routing decisions and auto-refreshed by the background job.
--
--   Current state:
--     - credential_model_index (regular table, NOT partitioned)
--     - credential_model_index_with_current_month (view, just selects from main table)
--     - No hot table exists yet
--
--   Problem:
--     - Without a hot table, the auto_index_refresher cannot write to hot table
--     - Gateway logs show: "credential_model_index_hot does not exist"
--
-- Solution:
--   1. Create credential_model_index_hot as an independent heap table
--   2. Create indexes for performance
--   3. Create promote function for data migration
--   4. Update the with_current_month view
--
-- Note: credential_model_index is NOT partitioned (unlike other tables).
-- The hot table will hold recent data, and the main table holds historical data.
--
-- Author: ACC team (2026-07-06)

BEGIN;

-- ============================================================
-- 1. Create hot table
-- ============================================================

CREATE TABLE IF NOT EXISTS credential_model_index_hot (
    -- Copy structure from credential_model_index
    LIKE credential_model_index INCLUDING DEFAULTS INCLUDING CONSTRAINTS
);

COMMENT ON TABLE credential_model_index_hot IS
'Hot data table for credential_model_index (recent 5min rollups).
Completely independent from main table.
Data older than 7 days can be migrated to main table by promote_credential_model_index_hot_to_partition().
Created by migration 354 (2026-07-06).';

-- ============================================================
-- 2. Create indexes
-- ============================================================

-- 2.1 Bucket index (time-based queries)
CREATE INDEX IF NOT EXISTS idx_credential_model_index_hot_bucket 
  ON credential_model_index_hot (bucket DESC);

-- 2.2 Credential + model index (routing queries)
CREATE INDEX IF NOT EXISTS idx_credential_model_index_hot_cred_model
  ON credential_model_index_hot (credential_id, raw_model);

-- 2.3 Unique constraint (supports UPSERT)
CREATE UNIQUE INDEX IF NOT EXISTS idx_credential_model_index_hot_unique
  ON credential_model_index_hot (bucket, credential_id, raw_model);

-- 2.4 Score indexes (routing decisions)
CREATE INDEX IF NOT EXISTS idx_credential_model_index_hot_score_smart
  ON credential_model_index_hot (score_smart DESC NULLS LAST);

CREATE INDEX IF NOT EXISTS idx_credential_model_index_hot_score_speed
  ON credential_model_index_hot (score_speed_first DESC NULLS LAST);

CREATE INDEX IF NOT EXISTS idx_credential_model_index_hot_score_cost
  ON credential_model_index_hot (score_cost_first DESC NULLS LAST);

DO $$ BEGIN RAISE NOTICE 'Created indexes on credential_model_index_hot'; END $$;

-- ============================================================
-- 3. Create promote function
-- ============================================================

CREATE OR REPLACE FUNCTION promote_credential_model_index_hot_to_partition(
    p_retention interval DEFAULT '7 days',
    p_batch_size int DEFAULT 5000
) RETURNS bigint
LANGUAGE plpgsql
AS $$
DECLARE
    v_moved bigint := 0;
BEGIN
    -- Move rows older than retention to the main table
    WITH batch AS (
        SELECT *
        FROM credential_model_index_hot
        WHERE bucket < now() - p_retention
        ORDER BY bucket
        LIMIT p_batch_size
    ),
    deleted AS (
        DELETE FROM credential_model_index_hot
        WHERE (bucket, credential_id, raw_model) IN 
              (SELECT bucket, credential_id, raw_model FROM batch)
        RETURNING *
    )
    INSERT INTO credential_model_index
    SELECT * FROM deleted
    ON CONFLICT (bucket, credential_id, raw_model) DO NOTHING;
    
    GET DIAGNOSTICS v_moved = ROW_COUNT;
    RETURN v_moved;
END;
$$;

COMMENT ON FUNCTION promote_credential_model_index_hot_to_partition(interval, int) IS
'Migrate cold rows from credential_model_index_hot to main table.
Returns the number of rows moved. Call in a loop until it returns 0.
Created by migration 354 (2026-07-06).';

-- ============================================================
-- 4. Create or replace the with_current_month view
-- ============================================================

DROP VIEW IF EXISTS credential_model_index_with_current_month CASCADE;

CREATE VIEW credential_model_index_with_current_month AS
SELECT * FROM credential_model_index_hot
UNION ALL
SELECT * FROM credential_model_index;

COMMENT ON VIEW credential_model_index_with_current_month IS
'Combines hot data with historical data from credential_model_index.
Use this view for routing decisions that need recent + historical data.
Created by migration 354 (2026-07-06).';

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
    SELECT count(*) INTO hot_count FROM credential_model_index_hot;
    RAISE NOTICE 'credential_model_index_hot contains % rows', hot_count;
    
    -- Check view
    SELECT count(*) INTO view_count FROM credential_model_index_with_current_month;
    RAISE NOTICE 'credential_model_index_with_current_month view works (% rows)', view_count;
    
    -- Check promote function
    SELECT EXISTS (
        SELECT 1 FROM pg_proc 
        WHERE proname = 'promote_credential_model_index_hot_to_partition'
    ) INTO func_exists;
    
    IF func_exists THEN
        RAISE NOTICE 'Migration 354 verification PASSED';
        RAISE NOTICE '  - hot table: % rows (storage: heap)', hot_count;
        RAISE NOTICE '  - view: exists';
        RAISE NOTICE '  - promote function: exists';
    ELSE
        RAISE EXCEPTION 'Migration 354 verification FAILED: promote function not created';
    END IF;
END $$;

COMMIT;

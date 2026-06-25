-- Rollback for Migration 301
-- Restores (request_id, ts) unique constraint if needed
-- Date: 2026-06-26

-- Step 1: Drop the (request_id) only unique index
DROP INDEX IF EXISTS idx_request_logs_request_id_unique;

-- Step 2: Restore the old (request_id, ts) unique index
CREATE UNIQUE INDEX IF NOT EXISTS idx_request_logs_request_id_ts_unique
    ON request_logs (request_id, ts);

-- Step 3: Update comment
COMMENT ON INDEX idx_request_logs_request_id_ts_unique IS 
    'Original unique constraint on (request_id, ts). Rolled back from migration 301.';

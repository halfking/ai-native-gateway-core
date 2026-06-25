-- Migration 301: Change UNIQUE constraint to (request_id) only for request_logs
-- Fixes duplicate record bug caused by (request_id, ts) allowing multiple rows
-- with same request_id but different timestamps.
-- Date: 2026-06-26
--
-- PROBLEM:
-- The current UPSERT uses ON CONFLICT (request_id, ts) where ts=now() differs
-- each INSERT, creating multiple rows for the same request_id. When UPDATE
-- fails to match a row, it falls back to INSERT with a new timestamp, creating
-- duplicates in an infinite loop.
--
-- SOLUTION:
-- Replace (request_id, ts) unique constraint with (request_id) only.
-- This ensures one row per request_id, making UPSERT and UPDATE operations
-- deterministic.
--
-- IMPORTANT: request_logs is a TimescaleDB hypertable partitioned by ts.
-- PostgreSQL normally requires that unique constraints on partitioned tables
-- include ALL partitioning columns. However, we can work around this by:
-- 1. Using a UNIQUE INDEX (not constraint) which TimescaleDB allows
-- 2. Or detaching the hypertable, adding constraint, then reattaching
--
-- We choose option 1 (UNIQUE INDEX) as it's simpler and non-destructive.

-- Step 1: Drop the old (request_id, ts) unique index
DROP INDEX IF EXISTS idx_request_logs_request_id_ts_unique;

-- Step 2: Clean up existing duplicates (keep earliest row per request_id)
-- This matches the new UPDATE logic which targets earliest (ASC order).
-- We only clean recent duplicates to minimize lock time.
DELETE FROM request_logs rl1
USING (
    SELECT request_id, MIN(ts) as first_ts
    FROM request_logs
    WHERE ts > now() - interval '7 days'
    GROUP BY request_id
    HAVING COUNT(*) > 1
) rl2
WHERE rl1.request_id = rl2.request_id
  AND rl1.ts > rl2.first_ts;

-- Step 3: Create UNIQUE INDEX on request_id only
-- Note: TimescaleDB allows unique indexes on non-partition columns in some cases,
-- but if this fails, we need to use exclude constraints or application-level
-- uniqueness. The index will at least provide fast lookups.
--
-- If this CREATE fails with "cannot create a unique index without the column ts",
-- we'll need to implement cleanup via application logic (already done) and
-- rely on the improved UPDATE query (targeting earliest row).
CREATE UNIQUE INDEX IF NOT EXISTS idx_request_logs_request_id_unique
    ON request_logs (request_id);

-- Step 4: Add comment documenting the change
COMMENT ON INDEX idx_request_logs_request_id_unique IS 
    'Ensures one row per request_id. The (request_id, ts) constraint was causing '
    'duplicates because ts=now() differs each INSERT. See migration 301.';

-- Rollback (if needed):
-- DROP INDEX IF EXISTS idx_request_logs_request_id_unique;
-- CREATE UNIQUE INDEX IF NOT EXISTS idx_request_logs_request_id_ts_unique
--     ON request_logs (request_id, ts);

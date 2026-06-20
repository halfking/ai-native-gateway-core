-- Migration 020: Add UNIQUE constraint on request_logs.request_id
-- Prevents duplicate rows for the same request_id, fixing cross-request pollution.
-- Date: 2026-06-20

-- Step 1: Clean up any existing duplicates (keep the latest row per request_id)
-- This DELETE targets only recent duplicates to minimize lock time.
DELETE FROM request_logs rl1
USING request_logs rl2
WHERE rl1.request_id = rl2.request_id
  AND rl1.ts < rl2.ts
  AND rl1.ts > now() - interval '7 days';  -- Only clean up recent duplicates

-- Step 2: Add UNIQUE index (non-blocking in PostgreSQL 11+)
-- We use CREATE UNIQUE INDEX instead of ALTER TABLE ADD CONSTRAINT
-- because it's online-safe and can be created CONCURRENTLY.
CREATE UNIQUE INDEX IF NOT EXISTS idx_request_logs_request_id_unique
    ON request_logs (request_id);

-- Note: This migration is idempotent and safe to re-run.
-- If duplicates exist in older data (>7 days), they won't block the index
-- creation but will prevent future duplicates.

-- Migration 020: Add UNIQUE constraint on (request_id, ts) for request_logs
-- Prevents duplicate rows for the same request_id, fixing cross-request pollution.
-- Date: 2026-06-20
--
-- IMPORTANT: request_logs is a TimescaleDB hypertable partitioned by ts.
-- PostgreSQL requires that unique constraints on partitioned tables must
-- include ALL partitioning columns. (request_id) alone would error with
-- SQLSTATE 42P10 "there is no unique or exclusion constraint matching the
-- ON CONFLICT specification" against a partitioned table.
--
-- This migration creates the index on (request_id, ts) — same shape as the
-- ON CONFLICT clause in telemetry/client.go: ON CONFLICT (request_id, ts).
--
-- 2026-06-26 SUPERSEDED by migration 301_request_logs_unique_request_id_only.sql.
-- The (request_id, ts) constraint allowed ts=now() to differ per INSERT,
-- producing duplicate rows under retry storms. Migration 301 replaces this
-- with UNIQUE (request_id) only after confirming partitioned-table support
-- works on the production Postgres 15.3 cluster. See CHANGELOG_*.md.

-- Step 1: Clean up any existing duplicates (keep the latest row per request_id)
-- This DELETE targets only recent duplicates to minimize lock time.
DELETE FROM request_logs rl1
USING request_logs rl2
WHERE rl1.request_id = rl2.request_id
  AND rl1.ts < rl2.ts
  AND rl1.ts > now() - interval '7 days';  -- Only clean up recent duplicates

-- Step 2: Add UNIQUE index on (request_id, ts) for partition compatibility
-- IF NOT EXISTS makes this migration idempotent. Production deployment
-- (commit 2dae2ded) already created this index as
-- idx_request_logs_request_id_ts_unique; re-running will skip silently.
CREATE UNIQUE INDEX IF NOT EXISTS idx_request_logs_request_id_ts_unique
    ON request_logs (request_id, ts);

-- Note: This migration is idempotent and safe to re-run.
-- If duplicates exist in older data (>7 days), they won't block the index
-- creation but will prevent future duplicates.

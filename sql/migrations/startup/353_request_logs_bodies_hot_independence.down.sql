-- Migration 353 rollback: request_logs_bodies hot table independence
--
-- Rollback order (reverse of up migration):
--   1. Drop the with_current_month view
--   2. Drop the promote function
--   3. Drop indexes
--   4. Drop the hot table
--
-- Author: ACC team (2026-07-06)

BEGIN;

-- Drop view
DROP VIEW IF EXISTS request_logs_bodies_with_current_month CASCADE;

-- Drop promote function
DROP FUNCTION IF EXISTS promote_request_logs_bodies_hot_to_partition(interval, int);

-- Drop hot table (indexes are dropped automatically)
DROP TABLE IF EXISTS request_logs_bodies_hot CASCADE;

COMMIT;

-- Migration: 302_fix_is_auto_request_null.sql
-- Purpose: Fix is_auto_request NULL values to enable analytics queries
-- Date: 2026-06-26
-- Issue: Analytics endpoints return empty data because is_auto_request=NULL
--        doesn't match SQL condition `is_auto_request = FALSE`

-- Background:
--   The analytics queries in admin/analytics.go use:
--     AND (is_auto_request = TRUE OR (is_auto_request = FALSE AND ...))
--   When is_auto_request is NULL (which happens for all non-auto requests
--   before the relay/request_log_pipeline.go fix), the FALSE condition
--   doesn't match due to SQL three-valued logic, causing those rows to be
--   excluded from analytics results.

-- Strategy:
--   1. Update NULL values to FALSE for rows that clearly aren't auto requests
--   2. Keep NULL for genuinely unknown cases (e.g., very old rows before
--      the is_auto_request column existed)

-- Step 1: Set is_auto_request=FALSE for rows where client_model is set
--         and auto_profile is NULL (indicating a specified-model request)
UPDATE request_logs
SET is_auto_request = FALSE
WHERE is_auto_request IS NULL
  AND client_model IS NOT NULL
  AND client_model <> ''
  AND (auto_profile IS NULL OR auto_profile = '')
  AND (task_type IS NULL OR task_type = '')
  AND ts >= NOW() - INTERVAL '30 days';

-- Step 2: Set is_auto_request=FALSE for rows that have routing_decision_log
--         entries indicating non-auto routing (where client_model was used)
UPDATE request_logs rl
SET is_auto_request = FALSE
FROM routing_decision_log rdl
WHERE rl.request_id = rdl.request_id
  AND rl.is_auto_request IS NULL
  AND rdl.client_model IS NOT NULL
  AND rdl.client_model <> ''
  AND (rdl.outbound_model IS NULL OR rdl.outbound_model = rdl.client_model)
  AND rl.ts >= NOW() - INTERVAL '30 days';

-- Note: We only update recent data (30 days) to avoid long-running
-- transactions on large historical datasets. Older NULL rows will be
-- handled by the query logic adjustment if needed.

-- Verification queries (uncomment to run):
-- SELECT COUNT(*) as total,
--        COUNT(*) FILTER (WHERE is_auto_request = TRUE) as auto_true,
--        COUNT(*) FILTER (WHERE is_auto_request = FALSE) as auto_false,
--        COUNT(*) FILTER (WHERE is_auto_request IS NULL) as auto_null
-- FROM request_logs
-- WHERE ts >= NOW() - INTERVAL '7 days';

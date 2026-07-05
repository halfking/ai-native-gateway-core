-- Migration 340: Create partition query VIEWs
--
-- Background:
--   Migration 337 DETACHed current and future monthly partitions so that
--   *_default partitions can accept all new writes. After DETACH, SELECT * FROM
--   <parent> no longer automatically includes the detached partition's data.
--
--   This migration creates standard *_with_current_month VIEWs that use
--   UNION ALL to explicitly aggregate:
--     1. Parent table (auto-aggregates all ATTACHED historical partitions)
--     2. Current month DETACHED partition (2026_07)
--     3. *_default partition (hot data, < 7 days)
--
-- Maintenance:
--   These VIEWs must be updated on the 1st of each month to include the
--   new month's partition in the UNION ALL. The bg/partition_manager.go
--   scheduler is the long-term solution for automated VIEW updates.
--
--   Manual update example (August 1st):
--     DROP VIEW request_logs_with_current_month;
--     CREATE VIEW request_logs_with_current_month AS
--       SELECT * FROM request_logs
--       UNION ALL SELECT * FROM request_logs_2026_08  -- new month
--       UNION ALL SELECT * FROM request_logs_default;
--
-- Applicable tables:
--   request_logs, request_wal, usage_ledger,
--   routing_decision_log, credential_model_index,
--   request_logs_bodies, credit_ledger, tool_usage_stats
--
-- Author: llm-gateway-ops (2026-07-05)

BEGIN;

-- ============================================================
-- 1. request_logs_with_current_month
-- ============================================================
-- Usage: Cross-month queries for request_logs
-- Example: SELECT * FROM request_logs_with_current_month
--            WHERE ts >= '2026-06-15' ORDER BY ts DESC LIMIT 100;

CREATE OR REPLACE VIEW request_logs_with_current_month AS
SELECT * FROM request_logs
    -- Parent table auto-aggregates all ATTACHED historical partitions (2026_06 and earlier)
UNION ALL
SELECT * FROM request_logs_2026_07
    -- Current month DETACHED partition
UNION ALL
SELECT * FROM request_logs_default;
    -- Hot data: < 7 days old, heap storage, supports UPDATE/DELETE

COMMENT ON VIEW request_logs_with_current_month IS
'Cross-month query VIEW for request_logs. Aggregates:
  1. request_logs parent (ATTACHED historical partitions)
  2. request_logs_2026_07 (current month, DETACHED)
  3. request_logs_default (hot data, < 7 days)
Must be updated on the 1st of each month to include the new month''s partition.
Created by migration 340 (2026-07-05).';

-- ============================================================
-- 2. request_wal_with_current_month
-- ============================================================
CREATE OR REPLACE VIEW request_wal_with_current_month AS
SELECT * FROM request_wal
UNION ALL
SELECT * FROM request_wal_2026_07
UNION ALL
SELECT * FROM request_wal_default;

COMMENT ON VIEW request_wal_with_current_month IS
'Cross-month query VIEW for request_wal. Aggregates parent + 2026_07 + default.
Must be updated on the 1st of each month. Created by migration 340 (2026-07-05).';

-- ============================================================
-- 3. usage_ledger_with_current_month
-- ============================================================
CREATE OR REPLACE VIEW usage_ledger_with_current_month AS
SELECT * FROM usage_ledger
UNION ALL
SELECT * FROM usage_ledger_2026_07
UNION ALL
SELECT * FROM usage_ledger_default;

COMMENT ON VIEW usage_ledger_with_current_month IS
'Cross-month query VIEW for usage_ledger. Aggregates parent + 2026_07 + default.
Must be updated on the 1st of each month. Created by migration 340 (2026-07-05).';

-- ============================================================
-- 4. routing_decision_log_with_current_month
-- ============================================================
CREATE OR REPLACE VIEW routing_decision_log_with_current_month AS
SELECT * FROM routing_decision_log
UNION ALL
SELECT * FROM routing_decision_log_2026_07
UNION ALL
SELECT * FROM routing_decision_log_default;

COMMENT ON VIEW routing_decision_log_with_current_month IS
'Cross-month query VIEW for routing_decision_log. Aggregates parent + 2026_07 + default.
Must be updated on the 1st of each month. Created by migration 340 (2026-07-05).';

-- ============================================================
-- 5. credential_model_index_with_current_month
-- ============================================================
CREATE OR REPLACE VIEW credential_model_index_with_current_month AS
SELECT * FROM credential_model_index
UNION ALL
SELECT * FROM credential_model_index_2026_07
UNION ALL
SELECT * FROM credential_model_index_default;

COMMENT ON VIEW credential_model_index_with_current_month IS
'Cross-month query VIEW for credential_model_index. Aggregates parent + 2026_07 + default.
Must be updated on the 1st of each month. Created by migration 340 (2026-07-05).';

-- ============================================================
-- 6. request_logs_bodies_with_current_month
-- ============================================================
CREATE OR REPLACE VIEW request_logs_bodies_with_current_month AS
SELECT * FROM request_logs_bodies
UNION ALL
SELECT * FROM request_logs_bodies_2026_07
UNION ALL
SELECT * FROM request_logs_bodies_default;

COMMENT ON VIEW request_logs_bodies_with_current_month IS
'Cross-month query VIEW for request_logs_bodies. Aggregates parent + 2026_07 + default.
Must be updated on the 1st of each month. Created by migration 340 (2026-07-05).';

-- ============================================================
-- 7. credit_ledger_with_current_month
-- ============================================================
CREATE OR REPLACE VIEW credit_ledger_with_current_month AS
SELECT * FROM credit_ledger
UNION ALL
SELECT * FROM credit_ledger_2026_07
UNION ALL
SELECT * FROM credit_ledger_default;

COMMENT ON VIEW credit_ledger_with_current_month IS
'Cross-month query VIEW for credit_ledger. Aggregates parent + 2026_07 + default.
Must be updated on the 1st of each month. Created by migration 340 (2026-07-05).';

-- ============================================================
-- 8. tool_usage_stats_with_current_month
-- ============================================================
CREATE OR REPLACE VIEW tool_usage_stats_with_current_month AS
SELECT * FROM tool_usage_stats
UNION ALL
SELECT * FROM tool_usage_stats_2026_07
UNION ALL
SELECT * FROM tool_usage_stats_default;

COMMENT ON VIEW tool_usage_stats_with_current_month IS
'Cross-month query VIEW for tool_usage_stats. Aggregates parent + 2026_07 + default.
Must be updated on the 1st of each month. Created by migration 340 (2026-07-05).';

-- ============================================================
-- 9. Verification
-- ============================================================
DO $$
DECLARE
    expected_views TEXT[] := ARRAY[
        'request_logs_with_current_month',
        'request_wal_with_current_month',
        'usage_ledger_with_current_month',
        'routing_decision_log_with_current_month',
        'credential_model_index_with_current_month',
        'request_logs_bodies_with_current_month',
        'credit_ledger_with_current_month',
        'tool_usage_stats_with_current_month'
    ];
    v TEXT;
    missing_views TEXT := '';
BEGIN
    FOREACH v IN ARRAY expected_views LOOP
        IF NOT EXISTS (
            SELECT 1 FROM pg_class c
            JOIN pg_namespace n ON n.oid = c.relnamespace
            WHERE c.relname = v
              AND n.nspname = 'public'
              AND c.relkind = 'v'  -- view
        ) THEN
            missing_views := missing_views || v || ', ';
        END IF;
    END LOOP;

    IF missing_views <> '' THEN
        RAISE EXCEPTION 'Migration 340: missing VIEWs: %', missing_views;
    END IF;

    RAISE NOTICE 'Migration 340 complete: all 8 *_with_current_month VIEWs created.';
END $$;

COMMIT;

-- ============================================================
-- Usage Examples
-- ============================================================
--
-- Query last 30 days of request logs:
--   SELECT * FROM request_logs_with_current_month
--    WHERE ts >= now() - interval '30 days'
--    ORDER BY ts DESC LIMIT 100;
--
-- Aggregate usage by tenant for current month:
--   SELECT tenant_id, SUM(total_tokens) AS tokens
--     FROM usage_ledger_with_current_month
--    WHERE ts >= date_trunc('month', now())
--    GROUP BY tenant_id;
--
-- Check routing decisions for a specific credential:
--   SELECT * FROM routing_decision_log_with_current_month
--    WHERE chosen_credential_id = $1
--      AND ts >= now() - interval '7 days';
--
-- ============================================================
-- Monthly Update Procedure (for automation)
-- ============================================================
--
-- To update VIEWs on the 1st of each month, run:
--
--   SELECT update_partition_view('request_logs', '2026', '08');
--
--   -- This will:
--   -- 1. DROP the existing VIEW
--   -- 2. CREATE new VIEW with the new month's partition
--   -- 3. RETURN the new VIEW definition
--
-- The update_partition_view() function is planned for migration 341.

\echo ''
\echo 'Migration 340 complete:'
\echo '  Created 8 *_with_current_month VIEWs'
\echo '  All aggregate parent + current month (2026_07) + default'
\echo ''
\echo 'Next steps:'
\echo '  1. Update application queries to use VIEWs'
\echo '  2. Automate monthly VIEW updates in bg/partition_manager.go'
\echo '  3. Test: SELECT count(*) FROM request_logs_with_current_month'
\echo ''

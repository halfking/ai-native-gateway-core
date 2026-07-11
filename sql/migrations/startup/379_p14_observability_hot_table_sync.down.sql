-- Rollback: Migration 379: P1.4 Observability Hot Table Sync
--
-- Purpose: Remove 10 P1.4 observability fields from both request_logs parent
--          and request_logs_hot independent table.
--
-- Safety: This down migration only drops columns added by migration 379.
--         It does NOT drop columns that existed before (e.g., from earlier
--         observability work). The IF EXISTS guards ensure idempotency.
--
-- Author: llm-gateway-ops (2026-07-11)

BEGIN;

-- ════════════════════════════════════════════════════════════
-- Part 1: Drop indexes (before dropping columns)
-- ════════════════════════════════════════════════════════════

DROP INDEX IF EXISTS idx_request_logs_hot_client_ip;
DROP INDEX IF EXISTS idx_request_logs_hot_agent_type;
DROP INDEX IF EXISTS idx_request_logs_hot_protocol_conversion;

-- ════════════════════════════════════════════════════════════
-- Part 2: Drop columns from request_logs_hot
-- ════════════════════════════════════════════════════════════

ALTER TABLE request_logs_hot
    DROP COLUMN IF EXISTS client_ip,
    DROP COLUMN IF EXISTS client_forwarded_for,
    DROP COLUMN IF EXISTS agent_name,
    DROP COLUMN IF EXISTS agent_type,
    DROP COLUMN IF EXISTS api_key_fingerprint,
    DROP COLUMN IF EXISTS session_title,
    DROP COLUMN IF EXISTS task_id,
    DROP COLUMN IF EXISTS upstream_endpoint,
    DROP COLUMN IF EXISTS upstream_protocol,
    DROP COLUMN IF EXISTS protocol_conversion;

-- ════════════════════════════════════════════════════════════
-- Part 3: Drop columns from request_logs parent
-- ════════════════════════════════════════════════════════════
-- Note: Dropping columns from a partitioned parent table cascades
--       to all attached partitions automatically in PostgreSQL 11+.

ALTER TABLE request_logs
    DROP COLUMN IF EXISTS client_ip,
    DROP COLUMN IF EXISTS client_forwarded_for,
    DROP COLUMN IF EXISTS agent_name,
    DROP COLUMN IF EXISTS agent_type,
    DROP COLUMN IF EXISTS api_key_fingerprint,
    DROP COLUMN IF EXISTS session_title,
    DROP COLUMN IF EXISTS task_id,
    DROP COLUMN IF EXISTS upstream_endpoint,
    DROP COLUMN IF EXISTS upstream_protocol,
    DROP COLUMN IF EXISTS protocol_conversion;

-- ════════════════════════════════════════════════════════════
-- Part 4: Recreate view (now without P1.4 columns)
-- ════════════════════════════════════════════════════════════

DROP VIEW IF EXISTS request_logs_with_current_month CASCADE;

CREATE VIEW request_logs_with_current_month AS
SELECT * FROM request_logs_hot    -- Hot table (0-7 days, independent)
UNION ALL
SELECT * FROM request_logs;        -- Parent table (all ATTACHED monthly partitions)

COMMENT ON VIEW request_logs_with_current_month IS
'Optimized query VIEW using hot table architecture (migration 341).
Rollback by migration 379.down: P1.4 observability fields removed.';

-- ════════════════════════════════════════════════════════════
-- Part 5: Validation
-- ════════════════════════════════════════════════════════════

DO $$
DECLARE
    remaining_parent TEXT[];
    remaining_hot TEXT[];
    p14_cols TEXT[] := ARRAY[
        'client_ip', 'client_forwarded_for', 'agent_name', 'agent_type',
        'api_key_fingerprint', 'session_title', 'task_id',
        'upstream_endpoint', 'upstream_protocol', 'protocol_conversion'
    ];
BEGIN
    -- Check parent columns removed
    SELECT ARRAY_AGG(column_name) INTO remaining_parent
    FROM information_schema.columns
    WHERE table_name = 'request_logs'
      AND table_schema = 'public'
      AND column_name = ANY(p14_cols);

    IF array_length(remaining_parent, 1) > 0 THEN
        RAISE EXCEPTION 'Failed to drop P1.4 columns from request_logs: %',
            array_to_string(remaining_parent, ', ');
    END IF;

    -- Check hot columns removed
    SELECT ARRAY_AGG(column_name) INTO remaining_hot
    FROM information_schema.columns
    WHERE table_name = 'request_logs_hot'
      AND table_schema = 'public'
      AND column_name = ANY(p14_cols);

    IF array_length(remaining_hot, 1) > 0 THEN
        RAISE EXCEPTION 'Failed to drop P1.4 columns from request_logs_hot: %',
            array_to_string(remaining_hot, ', ');
    END IF;

    -- Check indexes removed
    IF EXISTS (
        SELECT 1 FROM pg_indexes
        WHERE tablename = 'request_logs_hot'
          AND indexname IN (
              'idx_request_logs_hot_client_ip',
              'idx_request_logs_hot_agent_type',
              'idx_request_logs_hot_protocol_conversion'
          )
    ) THEN
        RAISE EXCEPTION 'Some P1.4 indexes still exist on request_logs_hot';
    END IF;

    RAISE NOTICE '═══════════════════════════════════════════════════════════';
    RAISE NOTICE 'Rollback 379: P1.4 Observability Sync SUCCESSFUL';
    RAISE NOTICE '═══════════════════════════════════════════════════════════';
    RAISE NOTICE '✓ Removed 10 P1.4 columns from request_logs parent';
    RAISE NOTICE '✓ Removed 10 P1.4 columns from request_logs_hot';
    RAISE NOTICE '✓ Removed 3 indexes from hot table';
    RAISE NOTICE '✓ Recreated request_logs_with_current_month VIEW';
    RAISE NOTICE '';
    RAISE NOTICE 'WARNING: Go code commit 8d0c07ef0 will fail without these columns.';
    RAISE NOTICE '         Rollback Go code or re-apply migration 379.';
END $$;

COMMIT;

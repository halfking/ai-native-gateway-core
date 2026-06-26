-- db/scripts/verify_request_logs_unique.sql
-- 2026-06-26 — verification script for the request_logs unique constraint fix.
--
-- Purpose:
--   Confirm that the (request_id)-only unique index has been applied to the
--   public.request_logs table on the target database. This index is the
--   cornerstone of the P0 hotfix for the duplicate-row bug (kaixuan's report
--   2026-06-26: glm-5.1 retry storm → 4 rows in request_logs, all updated to
--   nvidia nim success).
--
-- Usage:
--   psql $LLM_GATEWAY_DATABASE_URL -f db/scripts/verify_request_logs_unique.sql
--
-- Exit codes (when run via psql --set ON_ERROR_STOP=on):
--   0 = pass (new index present, old index absent, no duplicate rows)
--   != 0 = fail

\set ON_ERROR_STOP on

-- ─────────────────────────────────────────────────────────────────────────────
-- 1. The new UNIQUE INDEX on (request_id) must exist.
-- ─────────────────────────────────────────────────────────────────────────────
SELECT
    'check_new_index' AS check_name,
    EXISTS (
        SELECT 1 FROM pg_indexes
        WHERE schemaname = 'public'
          AND tablename  = 'request_logs'
          AND indexname  = 'idx_request_logs_request_id_unique'
    ) AS new_index_present,
    EXISTS (
        SELECT 1 FROM pg_indexes
        WHERE schemaname = 'public'
          AND tablename  = 'request_logs'
          AND indexname  = 'idx_request_logs_request_id_ts_unique'
    ) AS old_index_present;

-- If the new index is missing, surface a clear actionable error.
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_indexes
        WHERE schemaname = 'public'
          AND tablename  = 'request_logs'
          AND indexname  = 'idx_request_logs_request_id_unique'
    ) THEN
        RAISE EXCEPTION
            'idx_request_logs_request_id_unique index missing. '
            'Apply the fix: run db/migrations/301_request_logs_unique_request_id_only.sql '
            'OR restart the gateway so db.Open() applies ensureRequestLogsUniqueIndex. '
            'See CHANGELOG_request_logs_unique_id.md for context.';
    END IF;
END $$;

-- ─────────────────────────────────────────────────────────────────────────────
-- 2. The legacy UNIQUE INDEX on (request_id, ts) must be GONE.
--    If both indexes exist, the database is in an inconsistent state
--    (constraint mismatch would cause ON CONFLICT clauses to fail).
-- ─────────────────────────────────────────────────────────────────────────────
DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM pg_indexes
        WHERE schemaname = 'public'
          AND tablename  = 'request_logs'
          AND indexname  = 'idx_request_logs_request_id_ts_unique'
    ) THEN
        RAISE EXCEPTION
            'idx_request_logs_request_id_ts_unique still exists alongside the new index. '
            'This is an inconsistent state. Drop the old index: '
            'DROP INDEX IF EXISTS public.idx_request_logs_request_id_ts_unique;';
    END IF;
END $$;

-- ─────────────────────────────────────────────────────────────────────────────
-- 3. After migration 301, no duplicate (request_id) rows may remain in the
--    last 7 days. The migration cleans them, but verify here as a final guard.
-- ─────────────────────────────────────────────────────────────────────────────
DO $$
DECLARE
    dup_count BIGINT;
BEGIN
    SELECT COUNT(*) INTO dup_count
    FROM (
        SELECT request_id
        FROM request_logs
        WHERE ts > now() - interval '7 days'
        GROUP BY request_id
        HAVING COUNT(*) > 1
    ) dups;

    IF dup_count > 0 THEN
        RAISE EXCEPTION
            'Found % request_ids with duplicate rows in the last 7 days. '
            'Migration 301 cleanup step may have failed; inspect manually.', dup_count;
    END IF;
END $$;

-- ─────────────────────────────────────────────────────────────────────────────
-- 4. Summary: row counts and recent activity, for human inspection.
-- ─────────────────────────────────────────────────────────────────────────────
SELECT
    'request_logs_summary' AS section,
    COUNT(*)                                                    AS total_rows,
    COUNT(*) FILTER (WHERE ts > now() - interval '1 hour')      AS last_hour,
    COUNT(*) FILTER (WHERE ts > now() - interval '1 day')       AS last_day,
    COUNT(*) FILTER (WHERE ts > now() - interval '7 days')      AS last_7_days,
    COUNT(DISTINCT request_id) FILTER (WHERE ts > now() - interval '7 days') AS distinct_request_ids_7d
FROM request_logs;

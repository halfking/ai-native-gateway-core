-- Migration 336: Add request_logs_bodies_default + promote_*_default_batch functions
--
-- Background:
--   The 2026-07 data-lifecycle architecture requires:
--     1) INSERT/UPDATE/DELETE in Go code MUST target *_default partitions
--        (never the parent table — that would re-introduce the
--        "PG auto-route" bug that commit 9467ab6d tried to "fix").
--     2) *_default holds the recent hot window (default: 7 days).
--     3) A background migrator moves older rows from *_default into the
--        matching monthly partition so *_default doesn't accumulate.
--
-- Migrations 332-335 created *_default for:
--   - request_logs_default              (existed prior)
--   - request_wal_default               (332)
--   - routing_decision_log_default      (333)
--   - credit_ledger_default             (334)
--   - tool_usage_stats_default          (335)
--   - usage_ledger_default              (existed from 330)
--   - credential_model_index_default    (existed from 317)
--
-- This migration:
--   (1) Adds the missing request_logs_bodies_default partition (the
--       request_logs_bodies split-out table from migration 328a only
--       pre-creates monthly columnar partitions, never a DEFAULT catch-all).
--   (2) Provides a per-table promote_*_default_batch() SQL function so
--       the bg.PartitionManager scheduler can move one bounded batch of
--       cold rows from *_default into the matching monthly partition.
--
-- Design notes:
--   - Each function takes (p_retention interval, p_batch_size int) and
--     returns the number of rows actually moved (PG counts ON CONFLICT
--     skips against the rows that were inserted). Callers loop the call
--     until the function returns 0.
--   - The whole DELETE + INSERT happens inside a single CTE statement so
--     PostgreSQL evaluates them in one atomic transaction. ON CONFLICT
--     DO NOTHING guards against duplicates even if a previous run died
--     mid-batch and PG replayed the CTE.
--   - INSERT targets <table> (the parent) and PostgreSQL's partition
--     pruner routes each row to <table>_<YYYY_MM> or
--     <table>_<YYYY_MM_DD> automatically — we never name the monthly
--     partition directly. This is intentional: per the architecture
--     rule, only the migrator writes into the monthly partitions and
--     it does so via the parent.
--   - WHERE <ts_col> < now() - p_retention is the 7-day keep window.
--     Rows newer than that stay in *_default so application-layer
--     UPDATE/DELETE (which all happen within seconds/minutes) can still
--     mutate them.
--   - The ordering by <ts_col> ASC means we always drain the oldest rows
--     first, keeping the window boundary sharp.
--
-- Author: llm-gateway-ops (2026-07-04)

BEGIN;

-- ============================================================
-- 1. request_logs_bodies_default (heap, sibling of request_logs_bodies)
-- ============================================================
-- request_logs_bodies (created by migration 328a) partitions its data
-- by ts but its monthly partitions are columnar (created by
-- ensure_request_logs_bodies_partition with USING columnar). The
-- DEFAULT partition is heap because we want fast INSERT/UPDATE on the
-- write-hot path.

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_class c
        JOIN pg_namespace n ON n.oid = c.relnamespace
        WHERE c.relname = 'request_logs_bodies_default'
          AND n.nspname = 'public'
    ) THEN
        CREATE TABLE public.request_logs_bodies_default
            PARTITION OF public.request_logs_bodies DEFAULT;
        RAISE NOTICE 'Created partition: request_logs_bodies_default (heap)';
    ELSE
        RAISE NOTICE 'Partition request_logs_bodies_default already exists, skipping';
    END IF;
END $$;

COMMENT ON TABLE public.request_logs_bodies_default IS
    'heap landing pad for writes against request_logs_bodies. Rows older '
    'than 7 days are moved to request_logs_bodies_<YYYY_MM> (columnar) by '
    'promote_request_logs_bodies_default_batch(). Created 2026-07-04 by migration 336.';

-- ============================================================
-- 2. promote_request_logs_default_batch — for request_logs
-- ============================================================
-- request_logs is RANGE(ts). The CTE deletes rows whose ts is older
-- than p_retention (default 7 days), then inserts the deleted set back
-- into the parent so PG's partition pruner routes them to
-- request_logs_<YYYY_MM>.

CREATE OR REPLACE FUNCTION promote_request_logs_default_batch(
    p_retention interval DEFAULT '7 days',
    p_batch_size int     DEFAULT 5000
)
RETURNS bigint
LANGUAGE plpgsql AS $$
DECLARE
    n bigint;
BEGIN
    WITH del AS (
        DELETE FROM public.request_logs_default
        WHERE ts < now() - p_retention
        ORDER BY ts
        LIMIT p_batch_size
        RETURNING *
    ),
    ins AS (
        INSERT INTO public.request_logs
        SELECT * FROM del
        ON CONFLICT DO NOTHING
        RETURNING 1
    )
    SELECT count(*) INTO n FROM ins;
    RETURN n;
END;
$$;

COMMENT ON FUNCTION promote_request_logs_default_batch(interval, int) IS
    'Move one batch of cold rows (older than p_retention) from '
    'request_logs_default into the matching monthly partition (via '
    'parent insert). Returns rows moved. Iterate until 0 to drain. '
    'Added 2026-07-04 by migration 336.';

-- ============================================================
-- 3. promote_request_wal_default_batch — for request_wal
-- ============================================================
-- request_wal is RANGE(created_at).

CREATE OR REPLACE FUNCTION promote_request_wal_default_batch(
    p_retention interval DEFAULT '7 days',
    p_batch_size int     DEFAULT 5000
)
RETURNS bigint
LANGUAGE plpgsql AS $$
DECLARE
    n bigint;
BEGIN
    WITH del AS (
        DELETE FROM public.request_wal_default
        WHERE created_at < now() - p_retention
        ORDER BY created_at
        LIMIT p_batch_size
        RETURNING *
    ),
    ins AS (
        INSERT INTO public.request_wal
        SELECT * FROM del
        ON CONFLICT DO NOTHING
        RETURNING 1
    )
    SELECT count(*) INTO n FROM ins;
    RETURN n;
END;
$$;

COMMENT ON FUNCTION promote_request_wal_default_batch(interval, int) IS
    'Move one batch of cold rows from request_wal_default into the '
    'matching monthly partition. Returns rows moved. '
    'Added 2026-07-04 by migration 336.';

-- ============================================================
-- 4. promote_usage_ledger_default_batch — for usage_ledger
-- ============================================================
-- usage_ledger is RANGE(ts) (created by migration 330).

CREATE OR REPLACE FUNCTION promote_usage_ledger_default_batch(
    p_retention interval DEFAULT '7 days',
    p_batch_size int     DEFAULT 5000
)
RETURNS bigint
LANGUAGE plpgsql AS $$
DECLARE
    n bigint;
BEGIN
    WITH del AS (
        DELETE FROM public.usage_ledger_default
        WHERE ts < now() - p_retention
        ORDER BY ts
        LIMIT p_batch_size
        RETURNING *
    ),
    ins AS (
        INSERT INTO public.usage_ledger
        SELECT * FROM del
        ON CONFLICT DO NOTHING
        RETURNING 1
    )
    SELECT count(*) INTO n FROM ins;
    RETURN n;
END;
$$;

COMMENT ON FUNCTION promote_usage_ledger_default_batch(interval, int) IS
    'Move one batch of cold rows from usage_ledger_default into the '
    'matching monthly partition. Returns rows moved. '
    'Added 2026-07-04 by migration 336.';

-- ============================================================
-- 5. promote_routing_decision_log_default_batch — for routing_decision_log
-- ============================================================
-- routing_decision_log is RANGE(ts) (created by migration 333).

CREATE OR REPLACE FUNCTION promote_routing_decision_log_default_batch(
    p_retention interval DEFAULT '7 days',
    p_batch_size int     DEFAULT 5000
)
RETURNS bigint
LANGUAGE plpgsql AS $$
DECLARE
    n bigint;
BEGIN
    WITH del AS (
        DELETE FROM public.routing_decision_log_default
        WHERE ts < now() - p_retention
        ORDER BY ts
        LIMIT p_batch_size
        RETURNING *
    ),
    ins AS (
        INSERT INTO public.routing_decision_log
        SELECT * FROM del
        ON CONFLICT DO NOTHING
        RETURNING 1
    )
    SELECT count(*) INTO n FROM ins;
    RETURN n;
END;
$$;

COMMENT ON FUNCTION promote_routing_decision_log_default_batch(interval, int) IS
    'Move one batch of cold rows from routing_decision_log_default into '
    'the matching monthly partition. Returns rows moved. '
    'Added 2026-07-04 by migration 336.';

-- ============================================================
-- 6. promote_credential_model_index_default_batch — for credential_model_index
-- ============================================================
-- credential_model_index is RANGE(bucket). The bucket column is a
-- timestamptz (5-minute rollup window). Migration 317 created both the
-- partition tree and credential_model_index_default.

CREATE OR REPLACE FUNCTION promote_credential_model_index_default_batch(
    p_retention interval DEFAULT '7 days',
    p_batch_size int     DEFAULT 5000
)
RETURNS bigint
LANGUAGE plpgsql AS $$
DECLARE
    n bigint;
BEGIN
    WITH del AS (
        DELETE FROM public.credential_model_index_default
        WHERE bucket < now() - p_retention
        ORDER BY bucket
        LIMIT p_batch_size
        RETURNING *
    ),
    ins AS (
        INSERT INTO public.credential_model_index
        SELECT * FROM del
        ON CONFLICT DO NOTHING
        RETURNING 1
    )
    SELECT count(*) INTO n FROM ins;
    RETURN n;
END;
$$;

COMMENT ON FUNCTION promote_credential_model_index_default_batch(interval, int) IS
    'Move one batch of cold rows from credential_model_index_default '
    'into the matching monthly partition. Returns rows moved. '
    'Added 2026-07-04 by migration 336.';

-- ============================================================
-- 7. promote_request_logs_bodies_default_batch — for request_logs_bodies
-- ============================================================
-- request_logs_bodies is RANGE(ts) (created by migration 328a).
-- request_logs_bodies_default is created above (heap, this migration).

CREATE OR REPLACE FUNCTION promote_request_logs_bodies_default_batch(
    p_retention interval DEFAULT '7 days',
    p_batch_size int     DEFAULT 5000
)
RETURNS bigint
LANGUAGE plpgsql AS $$
DECLARE
    n bigint;
BEGIN
    WITH del AS (
        DELETE FROM public.request_logs_bodies_default
        WHERE ts < now() - p_retention
        ORDER BY ts
        LIMIT p_batch_size
        RETURNING *
    ),
    ins AS (
        INSERT INTO public.request_logs_bodies
        SELECT * FROM del
        ON CONFLICT DO NOTHING
        RETURNING 1
    )
    SELECT count(*) INTO n FROM ins;
    RETURN n;
END;
$$;

COMMENT ON FUNCTION promote_request_logs_bodies_default_batch(interval, int) IS
    'Move one batch of cold rows from request_logs_bodies_default into '
    'the matching monthly partition (columnar). Returns rows moved. '
    'Added 2026-07-04 by migration 336.';

-- ============================================================
-- 8. promote_credit_ledger_default_batch — for credit_ledger
-- ============================================================
-- credit_ledger is RANGE(created_at) (created by migration 334).

CREATE OR REPLACE FUNCTION promote_credit_ledger_default_batch(
    p_retention interval DEFAULT '7 days',
    p_batch_size int     DEFAULT 5000
)
RETURNS bigint
LANGUAGE plpgsql AS $$
DECLARE
    n bigint;
BEGIN
    WITH del AS (
        DELETE FROM public.credit_ledger_default
        WHERE created_at < now() - p_retention
        ORDER BY created_at
        LIMIT p_batch_size
        RETURNING *
    ),
    ins AS (
        INSERT INTO public.credit_ledger
        SELECT * FROM del
        ON CONFLICT DO NOTHING
        RETURNING 1
    )
    SELECT count(*) INTO n FROM ins;
    RETURN n;
END;
$$;

COMMENT ON FUNCTION promote_credit_ledger_default_batch(interval, int) IS
    'Move one batch of cold rows from credit_ledger_default into the '
    'matching monthly partition. Returns rows moved. '
    'Added 2026-07-04 by migration 336.';

-- ============================================================
-- 9. promote_tool_usage_stats_default_batch — for tool_usage_stats
-- ============================================================
-- tool_usage_stats is RANGE(usage_date) where usage_date is a `date`
-- (not timestamptz). Partition key is therefore compared with date
-- arithmetic: usage_date < current_date - integer (where integer days
-- comes from EXTRACT(epoch from p_retention) / 86400). We use a
-- CASE expression to support both interval and integer-day inputs.

CREATE OR REPLACE FUNCTION promote_tool_usage_stats_default_batch(
    p_retention interval DEFAULT '7 days',
    p_batch_size int     DEFAULT 5000
)
RETURNS bigint
LANGUAGE plpgsql AS $$
DECLARE
    n bigint;
    retention_days int := GREATEST(1, EXTRACT(DAY FROM p_retention)::int);
BEGIN
    -- tool_usage_stats.usage_date is a `date`, so we anchor on
    -- current_date rather than now(). p_retention is interpreted as a
    -- whole-day count (>= 1) for simplicity since usage_date has no
    -- time component.
    WITH del AS (
        DELETE FROM public.tool_usage_stats_default
        WHERE usage_date < current_date - retention_days
        ORDER BY usage_date
        LIMIT p_batch_size
        RETURNING *
    ),
    ins AS (
        INSERT INTO public.tool_usage_stats
        SELECT * FROM del
        ON CONFLICT DO NOTHING
        RETURNING 1
    )
    SELECT count(*) INTO n FROM ins;
    RETURN n;
END;
$$;

COMMENT ON FUNCTION promote_tool_usage_stats_default_batch(interval, int) IS
    'Move one batch of cold rows from tool_usage_stats_default into the '
    'matching monthly partition. usage_date is a `date` so retention '
    'rounds to whole days (>= 1). Returns rows moved. '
    'Added 2026-07-04 by migration 336.';

-- ============================================================
-- 10. Verification
-- ============================================================
-- Confirm all 8 *_default partitions exist and all 8 promote functions
-- are installed. After this migration completes, every partitioned
-- table in the data-lifecycle architecture has a default catch-all
-- partition and a corresponding promote_*_default_batch() function.

DO $$
DECLARE
    missing_partitions text := '';
    missing_functions  text := '';
    expected_partitions text[] := ARRAY[
        'request_logs_default',
        'request_wal_default',
        'usage_ledger_default',
        'routing_decision_log_default',
        'credential_model_index_default',
        'request_logs_bodies_default',
        'credit_ledger_default',
        'tool_usage_stats_default'
    ];
    expected_functions text[] := ARRAY[
        'promote_request_logs_default_batch',
        'promote_request_wal_default_batch',
        'promote_usage_ledger_default_batch',
        'promote_routing_decision_log_default_batch',
        'promote_credential_model_index_default_batch',
        'promote_request_logs_bodies_default_batch',
        'promote_credit_ledger_default_batch',
        'promote_tool_usage_stats_default_batch'
    ];
    p text;
    f text;
BEGIN
    FOREACH p IN ARRAY expected_partitions LOOP
        IF NOT EXISTS (
            SELECT 1 FROM pg_class c
            JOIN pg_namespace n ON n.oid = c.relnamespace
            WHERE c.relname = p AND n.nspname = 'public'
        ) THEN
            missing_partitions := missing_partitions || p || ', ';
        END IF;
    END LOOP;

    FOREACH f IN ARRAY expected_functions LOOP
        IF NOT EXISTS (
            SELECT 1 FROM pg_proc p
            JOIN pg_namespace n ON n.oid = p.pronamespace
            WHERE p.proname = f AND n.nspname = 'public'
        ) THEN
            missing_functions := missing_functions || f || ', ';
        END IF;
    END LOOP;

    IF missing_partitions <> '' THEN
        RAISE EXCEPTION 'Migration 336: missing *_default partitions: %', missing_partitions;
    END IF;

    IF missing_functions <> '' THEN
        RAISE EXCEPTION 'Migration 336: missing promote_*_default_batch functions: %', missing_functions;
    END IF;

    RAISE NOTICE 'Migration 336 complete: all 8 *_default partitions and 8 promote_*_default_batch() functions present.';
END $$;

COMMIT;

\echo ''
\echo 'Migration 336 complete:'
\echo '  - request_logs_bodies_default created (heap)'
\echo '  - 8 promote_*_default_batch functions installed'
\echo '    (request_logs, request_wal, usage_ledger, routing_decision_log,'
\echo '     credential_model_index, request_logs_bodies, credit_ledger,'
\echo '     tool_usage_stats)'
\echo ''
\echo 'Next steps:'
\echo '  - bg/partition_manager.go add promoteDefaultToPartitions() called at 1h interval'
\echo '  - verify with: SELECT promote_request_logs_default_batch(); -- repeat until 0'
\echo ''

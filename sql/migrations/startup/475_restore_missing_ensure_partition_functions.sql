-- Migration 475: recreate missing ensure_*_partition functions + ensure_cache_metrics_partition
--
-- Background
-- ──────────
-- Migrations 334/335/382/383 each defined an ensure_*_partition() function meant to
-- be called by bg.PartitionManager on every tick. Due to a description-key-based
-- reconcile logic (same pattern as the 431/432 silent-skip root cause in migration 474),
-- the function-creation blocks in those migrations were never applied to the production
-- DB (252, as confirmed by pg_dump taken 2026-08-04). The four tables themselves DO exist
-- and ARE RANGE-partitioned (inline CREATE + pre-created 2026_07 / _08 partitions), but
-- without the ensure functions the PartitionManager can never create 2026_09+ partitions
-- on its 24h ticks.
--
-- Migration 473 added 2026_09 / 2026_10 as a one-time patch, but that doesn't help
-- for 2026_11 and beyond. Registering these functions in Go's ensureSpecs() is meaningless
-- until the DB actually has the functions.
--
-- cache_metrics (added by migration 470, 2026-08-07) is a new table that never had an
-- ensure function at all.
--
-- What this migration does
-- ────────────────────────
-- 1. ensure_credit_ledger_partition(timestamptz)         — restore from migration 334
-- 2. ensure_tool_usage_stats_partition(timestamptz)      — restore from migration 335
-- 3. ensure_session_module_executions_partition(date)    — restore from migration 382
-- 4. ensure_dashboard_events_partition(date)             — restore from migration 383
-- 5. ensure_cache_metrics_partition(date)                — new (no prior migration)
--
-- After creating the functions, immediately materialise current + next month for all
-- five tables so the deploy itself doesn't depend on the next PartitionManager tick.
--
-- Parent-vs-partition indexes
-- ───────────────────────────
-- credit_ledger, tool_usage_stats, cache_metrics each have indexes declared on the PARENT
-- table (visible in pg_dump with "ON ONLY public.<parent>"). PostgreSQL auto-propagates
-- those parent indexes to every new PARTITION OF child — no explicit CREATE INDEX needed
-- in the ensure function.
--
-- session_module_executions and dashboard_access_events only have partition-level indexes
-- in the production dump. Their ensure functions must create named indexes per partition
-- (matching the naming convention from migrations 382/383).
--
-- Idempotent
-- ──────────
-- CREATE OR REPLACE FUNCTION is idempotent.
-- All partition-existence checks use pg_tables.tablename (NOT ::regclass cast) so
-- missing targets return NULL instead of raising an error — consistent with migrations
-- 472/473/474.

BEGIN;

-- ============================================================================
-- 1. ensure_credit_ledger_partition(timestamptz)
--    Restored from migration 334. Parent-level indexes auto-propagate.
-- ============================================================================

CREATE OR REPLACE FUNCTION public.ensure_credit_ledger_partition(
    target_month timestamp with time zone DEFAULT now()
)
RETURNS text
LANGUAGE plpgsql AS $$
DECLARE
    partition_name text;
    start_date timestamp with time zone;
    end_date   timestamp with time zone;
BEGIN
    start_date := date_trunc('month', target_month);
    end_date   := start_date + interval '1 month';
    partition_name := 'credit_ledger_' || to_char(start_date, 'YYYY_MM');

    IF EXISTS (
        SELECT 1 FROM pg_class c
        JOIN pg_namespace n ON c.relnamespace = n.oid
        WHERE c.relname = partition_name
          AND n.nspname = 'public'
    ) THEN
        RETURN partition_name || ' (already exists)';
    END IF;

    EXECUTE format(
        'CREATE TABLE public.%I PARTITION OF public.credit_ledger FOR VALUES FROM (%L) TO (%L)',
        partition_name, start_date, end_date
    );

    RAISE NOTICE 'ensure_credit_ledger_partition: created %', partition_name;
    RETURN partition_name;
END;
$$;

COMMENT ON FUNCTION public.ensure_credit_ledger_partition(timestamp with time zone) IS
'Ensure a monthly credit_ledger partition exists for the given month (heap storage).
Called by bg.PartitionManager on every tick for current + next month.
Parent-table indexes auto-propagate. Idempotent.
Originally in migration 334; recreated in 475 to fix production silent-skip.';

-- ============================================================================
-- 2. ensure_tool_usage_stats_partition(timestamptz)
--    Restored from migration 335. Parent-level indexes auto-propagate.
-- ============================================================================

CREATE OR REPLACE FUNCTION public.ensure_tool_usage_stats_partition(
    target_month timestamp with time zone DEFAULT now()
)
RETURNS text
LANGUAGE plpgsql AS $$
DECLARE
    partition_name text;
    start_date date;
    end_date   date;
BEGIN
    start_date := date_trunc('month', target_month)::date;
    end_date   := (start_date + interval '1 month')::date;
    partition_name := 'tool_usage_stats_' || to_char(start_date, 'YYYY_MM');

    IF EXISTS (
        SELECT 1 FROM pg_class c
        JOIN pg_namespace n ON c.relnamespace = n.oid
        WHERE c.relname = partition_name
          AND n.nspname = 'public'
    ) THEN
        RETURN partition_name || ' (already exists)';
    END IF;

    EXECUTE format(
        'CREATE TABLE public.%I PARTITION OF public.tool_usage_stats FOR VALUES FROM (%L) TO (%L)',
        partition_name, start_date, end_date
    );

    RAISE NOTICE 'ensure_tool_usage_stats_partition: created %', partition_name;
    RETURN partition_name;
END;
$$;

COMMENT ON FUNCTION public.ensure_tool_usage_stats_partition(timestamp with time zone) IS
'Ensure a monthly tool_usage_stats partition exists for the given month (heap storage).
Called by bg.PartitionManager on every tick for current + next month.
Parent-table indexes auto-propagate. Idempotent.
Originally in migration 335; recreated in 475 to fix production silent-skip.';

-- ============================================================================
-- 3. ensure_session_module_executions_partition(date)
--    Restored from migration 382. No parent-level indexes → must create per-partition.
--    Note: the original function also pre-created "next month" inline; we keep that
--    behaviour for backwards compatibility, though PartitionManager also calls this
--    for offset=1 independently.
-- ============================================================================

CREATE OR REPLACE FUNCTION public.ensure_session_module_executions_partition(
    target_date date DEFAULT NULL
)
RETURNS text
LANGUAGE plpgsql AS $$
DECLARE
    v_date           date;
    v_month_start    date;
    v_month_end      date;
    v_partition_name text;
BEGIN
    v_date        := COALESCE(target_date, CURRENT_DATE);
    v_month_start := DATE_TRUNC('month', v_date)::date;
    v_month_end   := (v_month_start + INTERVAL '1 month')::date;
    v_partition_name := 'session_module_executions_' || TO_CHAR(v_month_start, 'YYYY_MM');

    IF NOT EXISTS (SELECT 1 FROM pg_tables
                   WHERE schemaname = 'public' AND tablename = v_partition_name) THEN
        EXECUTE format(
            'CREATE TABLE public.%I PARTITION OF public.session_module_executions
             FOR VALUES FROM (%L) TO (%L)',
            v_partition_name, v_month_start, v_month_end
        );

        -- No parent-level indexes in prod dump → create per-partition indexes
        EXECUTE format(
            'CREATE INDEX idx_%s_session ON public.%I (gw_session_id, module_name)',
            v_partition_name, v_partition_name
        );
        EXECUTE format(
            'CREATE INDEX idx_%s_tenant ON public.%I (tenant_id, created_at DESC)',
            v_partition_name, v_partition_name
        );
    END IF;

    RETURN v_partition_name;
END;
$$;

COMMENT ON FUNCTION public.ensure_session_module_executions_partition(date) IS
'Ensure a monthly session_module_executions partition exists for the given date''s month.
Called by bg.PartitionManager on every tick for current + next month.
Creates per-partition named indexes (no parent-level indexes in prod schema). Idempotent.
Originally in migration 382; recreated in 475 to fix production silent-skip.';

-- ============================================================================
-- 4. ensure_dashboard_events_partition(date)
--    Restored from migration 383. No parent-level indexes → must create per-partition.
-- ============================================================================

CREATE OR REPLACE FUNCTION public.ensure_dashboard_events_partition(
    target_date date DEFAULT NULL
)
RETURNS text
LANGUAGE plpgsql AS $$
DECLARE
    v_date           date;
    v_month_start    date;
    v_month_end      date;
    v_partition_name text;
BEGIN
    v_date        := COALESCE(target_date, CURRENT_DATE);
    v_month_start := DATE_TRUNC('month', v_date)::date;
    v_month_end   := (v_month_start + INTERVAL '1 month')::date;
    v_partition_name := 'dashboard_access_events_' || TO_CHAR(v_month_start, 'YYYY_MM');

    IF NOT EXISTS (SELECT 1 FROM pg_tables
                   WHERE schemaname = 'public' AND tablename = v_partition_name) THEN
        EXECUTE format(
            'CREATE TABLE public.%I PARTITION OF public.dashboard_access_events
             FOR VALUES FROM (%L) TO (%L)',
            v_partition_name, v_month_start, v_month_end
        );

        -- No parent-level indexes in prod dump → create per-partition index
        EXECUTE format(
            'CREATE INDEX idx_%s_tenant ON public.%I (tenant_id, timestamp DESC)',
            v_partition_name, v_partition_name
        );
    END IF;

    RETURN v_partition_name;
END;
$$;

COMMENT ON FUNCTION public.ensure_dashboard_events_partition(date) IS
'Ensure a monthly dashboard_access_events partition exists for the given date''s month.
Called by bg.PartitionManager on every tick for current + next month.
Creates per-partition named indexes (no parent-level indexes in prod schema). Idempotent.
Originally in migration 383; recreated in 475 to fix production silent-skip.';

-- ============================================================================
-- 5. ensure_cache_metrics_partition(date)
--    New (no prior migration). cache_metrics was added in migration 470 (2026-08-07).
--    Parent-level indexes exist (migration 470) → auto-propagate, no explicit CREATE INDEX.
-- ============================================================================

CREATE OR REPLACE FUNCTION public.ensure_cache_metrics_partition(
    target_date date DEFAULT CURRENT_DATE
)
RETURNS text
LANGUAGE plpgsql AS $$
DECLARE
    month_start    date := date_trunc('month', target_date)::date;
    month_end      date := (date_trunc('month', target_date) + interval '1 month')::date;
    partition_name text := 'cache_metrics_' || to_char(month_start, 'YYYY_MM');
BEGIN
    IF EXISTS (
        SELECT 1 FROM pg_class c
        JOIN pg_namespace n ON c.relnamespace = n.oid
        WHERE c.relname = partition_name
          AND n.nspname = 'public'
    ) THEN
        RETURN partition_name || ' (already exists)';
    END IF;

    EXECUTE format(
        'CREATE TABLE public.%I PARTITION OF public.cache_metrics FOR VALUES FROM (%L) TO (%L)',
        partition_name, month_start, month_end
    );

    RAISE NOTICE 'ensure_cache_metrics_partition: created %', partition_name;
    RETURN partition_name;
END;
$$;

COMMENT ON FUNCTION public.ensure_cache_metrics_partition(date) IS
'Ensure a monthly cache_metrics partition exists for the given date''s month.
Called by bg.PartitionManager on every tick for current + next month.
Parent-table indexes from migration 470 auto-propagate. Idempotent.
Added 2026-08-07 in migration 475 (first ensure function for cache_metrics).';

-- ============================================================================
-- Immediately materialise current + next month for all 5 tables
-- so this deploy doesn't need to wait for the next PartitionManager tick.
-- Each function is idempotent: existing partitions are skipped silently.
-- ============================================================================

DO $$
DECLARE
    cur  date := CURRENT_DATE;
    nxt  date := (CURRENT_DATE + interval '1 month')::date;
BEGIN
    -- credit_ledger (timestamptz signature — pass as timestamptz)
    PERFORM public.ensure_credit_ledger_partition(cur::timestamptz);
    PERFORM public.ensure_credit_ledger_partition(nxt::timestamptz);

    -- tool_usage_stats (timestamptz signature)
    PERFORM public.ensure_tool_usage_stats_partition(cur::timestamptz);
    PERFORM public.ensure_tool_usage_stats_partition(nxt::timestamptz);

    -- session_module_executions (date signature)
    PERFORM public.ensure_session_module_executions_partition(cur);
    PERFORM public.ensure_session_module_executions_partition(nxt);

    -- dashboard_access_events (date signature)
    PERFORM public.ensure_dashboard_events_partition(cur);
    PERFORM public.ensure_dashboard_events_partition(nxt);

    -- cache_metrics (date signature)
    PERFORM public.ensure_cache_metrics_partition(cur);
    PERFORM public.ensure_cache_metrics_partition(nxt);

    RAISE NOTICE '475: current+next partitions ensured for all 5 tables';
END $$;

COMMIT;

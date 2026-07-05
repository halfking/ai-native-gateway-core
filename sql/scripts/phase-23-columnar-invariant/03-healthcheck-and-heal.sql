-- ============================================================
-- Phase 23 / 03 — columnar_healthcheck() and columnar_heal()
--
-- Two SQL functions used by:
--
--   - The application startup check (added in
--     gateway boot path; the equivalent of cmd/gateway/main.go
--     calls columnar_healthcheck() at startup).
--
--   - The daily cron (scripts/columnar-daily-cron.sh). The cron
--     SELECTs the result of columnar_healthcheck() and emits a
--     diff report; if any partition is non-compliant, it calls
--     columnar_heal().
--
-- columnar_healthcheck() returns one row per public partition with:
--   parent_name         text
--   partition_name      text
--   storage             text  ('columnar'|'heap')
--   expected            text  ('columnar'|'heap'|'unknown')
--   compliant           bool
--   total_size_bytes    bigint
--   n_live_tup          bigint
--
-- columnar_heal() converts all non-compliant INSERT-only partitions to
-- columnar and returns a per-partition summary.
-- ============================================================

-- ----------------------------------------------------------------
-- 1. columnar_healthcheck()
-- ----------------------------------------------------------------

CREATE OR REPLACE FUNCTION columnar_healthcheck()
RETURNS TABLE(
    parent_name text,
    partition_name text,
    storage text,
    expected text,
    compliant boolean,
    total_size_bytes bigint,
    n_live_tup bigint
)
LANGUAGE sql STABLE AS $$
    WITH config AS (
        SELECT
            -- INSERT-only parents: must be columnar
            columnar_insert_only_parents() AS should_be_columnar,
            -- UPDATE-heavy parents: must stay heap
            ARRAY['request_logs','request_wal','usage_ledger',
                  'request_logs_archive','request_wal_archive',
                  'usage_ledger_archive']::text[] AS should_be_heap
    ), partitions AS (
        SELECT
            p.relname AS parent_name,
            c.relname AS partition_name,
            CASE WHEN c.relam=(SELECT oid FROM pg_am WHERE amname='columnar') THEN 'columnar'
                 WHEN c.relam=(SELECT oid FROM pg_am WHERE amname='heap') THEN 'heap'
                 ELSE 'other' END AS storage,
            pg_total_relation_size(c.oid) AS total_size_bytes,
            (SELECT n_live_tup FROM pg_stat_user_tables WHERE relid=c.oid) AS n_live_tup
        FROM pg_inherits i
        JOIN pg_class p ON p.oid = i.inhparent
        JOIN pg_class c ON c.oid = i.inhrelid
        JOIN pg_namespace n ON n.oid = p.relnamespace
        WHERE n.nspname = 'public'
    )
    SELECT
        par.parent_name,
        par.partition_name,
        par.storage,
        CASE
            WHEN par.parent_name = ANY(cfg.should_be_columnar) THEN 'columnar'
            WHEN par.parent_name = ANY(cfg.should_be_heap)     THEN 'heap'
            ELSE 'unknown'
        END::text AS expected,
        (par.storage = CASE
            WHEN par.parent_name = ANY(cfg.should_be_columnar) THEN 'columnar'
            WHEN par.parent_name = ANY(cfg.should_be_heap)     THEN 'heap'
            ELSE NULL END) AS compliant,
        par.total_size_bytes,
        COALESCE(par.n_live_tup, 0)
    FROM partitions par, config cfg
    ORDER BY par.parent_name, par.partition_name;
$$;

COMMENT ON FUNCTION columnar_healthcheck() IS
'Returns columnar access-method status for every public partition. Used
by gateway startup check and the daily cron diff report. Phase 23 / 03.';

-- ----------------------------------------------------------------
-- 2. columnar_heal()
-- ----------------------------------------------------------------

CREATE OR REPLACE FUNCTION columnar_heal()
RETURNS TABLE(
    parent_name text,
    partition_name text,
    converted boolean,
    pre_size_bytes bigint,
    post_size_bytes bigint,
    error_message text
)
LANGUAGE plpgsql AS $$
DECLARE
    rec record;
    pre_size bigint;
    post_size bigint;
BEGIN
    FOR rec IN
        SELECT
            p.relname AS parent_name,
            c.relname AS partition_name,
            c.oid AS partition_oid
        FROM pg_inherits i
        JOIN pg_class p ON p.oid = i.inhparent
        JOIN pg_class c ON c.oid = i.inhrelid
        JOIN pg_am am ON am.oid = c.relam
        JOIN pg_namespace n ON n.oid = p.relnamespace
        WHERE n.nspname='public'
          AND am.amname = 'heap'
          AND p.relname = ANY(columnar_insert_only_parents())
    LOOP
        pre_size := pg_total_relation_size(rec.partition_oid);
        BEGIN
            EXECUTE format('ALTER TABLE public.%I SET ACCESS METHOD columnar',
                           rec.partition_name);
            post_size := pg_total_relation_size(rec.partition_oid);
            parent_name := rec.parent_name;
            partition_name := rec.partition_name;
            converted := true;
            pre_size_bytes := pre_size;
            post_size_bytes := post_size;
            error_message := NULL;
            RETURN NEXT;
        EXCEPTION WHEN OTHERS THEN
            parent_name := rec.parent_name;
            partition_name := rec.partition_name;
            converted := false;
            pre_size_bytes := pre_size;
            post_size_bytes := pre_size;
            error_message := SQLERRM;
            RETURN NEXT;
        END;
    END LOOP;
END;
$$;

COMMENT ON FUNCTION columnar_heal() IS
'Convert all heap partitions of INSERT-only parents to columnar. Skips
partitioned parents that have UPDATE traffic. Idempotent. Phase 23 / 03.';

-- ----------------------------------------------------------------
-- 3. columnar_drift_report() — compact summary used by the cron
-- ----------------------------------------------------------------

CREATE OR REPLACE FUNCTION columnar_drift_report()
RETURNS TABLE(
    parent_name text,
    compliant_count int,
    noncompliant_count int,
    total_size_bytes bigint,
    heap_size_bytes bigint,
    columnar_size_bytes bigint
)
LANGUAGE sql STABLE AS $$
    SELECT
        parent_name,
        count(*) FILTER (WHERE compliant) AS compliant_count,
        count(*) FILTER (WHERE NOT compliant) AS noncompliant_count,
        sum(total_size_bytes)::bigint AS total_size_bytes,
        sum(total_size_bytes) FILTER (WHERE storage='heap')::bigint AS heap_size_bytes,
        sum(total_size_bytes) FILTER (WHERE storage='columnar')::bigint AS columnar_size_bytes
    FROM columnar_healthcheck()
    GROUP BY parent_name
    ORDER BY parent_name;
$$;

COMMENT ON FUNCTION columnar_drift_report() IS
'Compact drift summary per parent table. Output is a single line per
parent — used by alerts. Phase 23 / 03.';

\echo ''
\echo 'Phase 23 / 03 complete: columnar_healthcheck / heal / drift_report installed.'

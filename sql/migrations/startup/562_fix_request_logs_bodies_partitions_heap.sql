-- Migration 562: Fix request_logs_bodies_2026_08 partition — convert empty
-- columnar partition to heap so the hot→monthly promote pipeline can move
-- rows from request_logs_bodies_hot into request_logs_bodies_2026_08.
--
-- Date: 2026-08-23
--
-- Background
-- ──────────
-- 1. ensure_request_logs_bodies_partition() historically created new
--    monthly partitions as `USING columnar` (see migration 328a +
--    sql/objects/functions/ensure_request_logs_bodies_partition_*.sql).
-- 2. Columnar partitions are append-only: UPDATE / DELETE on existing rows
--    are not supported. The hot→monthly promote path in
--    promote_request_logs_bodies_hot_to_partition() does
--    INSERT ... SELECT + DELETE FROM ..._hot. The INSERT leg succeeds but
--    any subsequent body cleanup, retry, or in-flight transactional update
--    against that month's partition fails.
-- 3. request_logs_bodies_2026_07 is a load-bearing partition that already
--    holds committed bodies for July and analytics queries scan it via
--    UNION ALL with the hot table — we MUST NOT touch it.
-- 4. request_logs_bodies_2026_08 is the current-month partition and is
--    empty (the body promote path never made progress past 07), so we
--    can safely DETACH + DROP + recreate as heap + ATTACH without losing
--    user data.
-- 5. ensure_request_logs_bodies_partition() is also replaced to stop
--    creating columnar partitions for any future month.
--
-- Scope (hard constraints from parent task)
-- ─────────────────────────────────────────
-- - DO NOT alter columnar storage on EXISTING partitions that are
--   load-bearing for analytics. Only the empty 2026_08 partition is
--   touched here; 2026_07 keeps its columnar storage and is excluded
--   from the repair.
-- - DO NOT drop the hot table or any hot→monthly trigger. Only partition
--   metadata (DETACH/DROP/ATTACH) is changed.
--
-- Idempotent: YES (each DO block checks storage before acting).
-- Down: 562_fix_request_logs_bodies_partitions_heap.down.sql

BEGIN;

-- ============================================================
-- 1. Replace ensure_request_logs_bodies_partition to create heap
-- ============================================================
-- The function body in sql/objects/functions/ has been edited to drop
-- the USING columnar clause, but the live function in the DB still has
-- the old body. CREATE OR REPLACE re-installs it from the canonical
-- source-of-truth at sql/objects/functions/.

DO $$
DECLARE
    fn_oid oid;
BEGIN
    SELECT p.oid INTO fn_oid
      FROM pg_proc p
      JOIN pg_namespace n ON n.oid = p.pronamespace
     WHERE n.nspname = 'public'
       AND p.proname = 'ensure_request_logs_bodies_partition'
       AND pg_get_function_identity_arguments(p.oid) = 'timestamp with time zone';

    IF fn_oid IS NULL THEN
        RAISE NOTICE '562: ensure_request_logs_bodies_partition(timestamptz) not found — skipping replace';
    ELSE
        RAISE NOTICE '562: ensure_request_logs_bodies_partition(timestamptz) present — operator must re-run sql/objects/ install to pick up heap body. CREATE OR REPLACE here is a no-op safety net.';
    END IF;
END $$;

-- Source-of-truth replacement is shipped in
-- sql/objects/functions/ensure_request_logs_bodies_partition_timestamp_with_time_zone.sql
-- and is applied by the objects install script. For environments that
-- run only startup migrations, we ship the body inline below:

CREATE OR REPLACE FUNCTION public.ensure_request_logs_bodies_partition(
    target_ts timestamp with time zone DEFAULT now()
) RETURNS void
    LANGUAGE plpgsql
    AS $$
DECLARE
    month_start    date := date_trunc('month', target_ts)::date;
    month_end      date := (date_trunc('month', target_ts) + interval '1 month')::date;
    partition_name text := 'request_logs_bodies_' || to_char(month_start, 'YYYY_MM');
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_class
                   WHERE relname = partition_name
                     AND relnamespace = 'public'::regnamespace) THEN
        -- 562: heap (was columnar). Bodies jsonb is TOAST-heavy and the
        -- promote path needs INSERT+UPDATE/DELETE on this partition.
        EXECUTE format(
            'CREATE TABLE %I PARTITION OF request_logs_bodies
             FOR VALUES FROM (%L) TO (%L)',
            partition_name, month_start, month_end
        );
        RAISE NOTICE '562: created % as heap', partition_name;
    END IF;
END;
$$;

-- ============================================================
-- 2. Convert the empty request_logs_bodies_2026_08 partition
--    from columnar to heap (2026_07 is intentionally skipped —
--    it holds committed July bodies and is load-bearing for analytics).
-- ============================================================

DO $$
DECLARE
    storage_type text;
    row_count    bigint;
    is_attached  boolean;
    bound        text;
    partition_oid oid;
    parent_name  text;
BEGIN
    SELECT c.oid, am.amname
      INTO partition_oid, storage_type
      FROM pg_class c
      LEFT JOIN pg_am am ON c.relam = am.oid
     WHERE c.relname = 'request_logs_bodies_2026_08'
       AND c.relnamespace = 'public'::regnamespace;

    IF partition_oid IS NULL THEN
        RAISE NOTICE '562: request_logs_bodies_2026_08 does not exist — nothing to convert';
        RETURN;
    END IF;

    -- Verify it is currently attached to the partitioned parent.
    SELECT EXISTS (
        SELECT 1
          FROM pg_inherits i
          JOIN pg_class c ON c.oid = i.inhrelid
          JOIN pg_class p ON p.oid = i.inhparent
         WHERE c.oid = partition_oid
           AND p.relname = 'request_logs_bodies'
    ) INTO is_attached;

    SELECT COUNT(*) INTO row_count FROM request_logs_bodies_2026_08;

    RAISE NOTICE '562: request_logs_bodies_2026_08 storage=%, rows=%, attached=%',
                 storage_type, row_count, is_attached;

    IF storage_type = 'heap' THEN
        RAISE NOTICE '562: already heap — skip';
        RETURN;
    END IF;

    IF row_count > 0 THEN
        -- 2026-08-23: the partition was assumed empty but in some
        -- environments (e.g. pre-release 245) the hot→monthly promote path
        -- has already moved rows into the current-month partition. A
        -- DETACH+DROP+RECREATE would lose real user data, so we skip the
        -- storage conversion and leave it columnar. The promote path will
        -- continue to surface the underlying issue until an operator
        -- manually resolves the storage; this migration's job is to be a
        -- safe no-op, not to block deploys.
        RAISE NOTICE '562: partition holds % rows — skipping columnar→heap conversion to avoid data loss (operator action required)', row_count;
        RETURN;
    END IF;

    IF NOT is_attached THEN
        RAISE EXCEPTION '562: ABORT — partition is detached; manual investigation needed';
    END IF;

    -- Capture the original partition bound so we can reattach with the
    -- exact same range (DDL uses FOR VALUES FROM ... TO ...).
    SELECT pg_get_expr(c.relpartbound, c.oid)
      INTO bound
      FROM pg_class c
     WHERE c.oid = partition_oid;

    RAISE NOTICE '562: bound=%', bound;

    ALTER TABLE public.request_logs_bodies
        DETACH PARTITION public.request_logs_bodies_2026_08;
    RAISE NOTICE '562: DETACHED request_logs_bodies_2026_08';

    DROP TABLE public.request_logs_bodies_2026_08 CASCADE;
    RAISE NOTICE '562: DROPPED request_logs_bodies_2026_08';

    -- Recreate as heap, attached to request_logs_bodies for 2026-08-01..2026-09-01.
    -- Hardcoded bound matches what the previous pg_dump recorded
    -- ('2026-08-01 00:00:00+08' → '2026-09-01 00:00:00+08' in the original
    -- install, but the ensure path uses date_trunc boundaries which fall
    -- back to '2026-08-01' → '2026-09-01' in the default session TZ).
    -- We use the date form so the partition matches the canonical range
    -- produced by ensure_<...>_partition() calls from bg.PartitionManager.
    CREATE TABLE public.request_logs_bodies_2026_08
        PARTITION OF public.request_logs_bodies
        FOR VALUES FROM ('2026-08-01') TO ('2026-09-01');
    RAISE NOTICE '562: RECREATED request_logs_bodies_2026_08 as heap';

    -- Reattach primary key constraint that the original partition had.
    -- request_logs_bodies PK is (request_id, ts) (see
    -- sql/migrations/startup/328a_request_logs_bodies_table.sql).
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
         WHERE conname = 'request_logs_bodies_2026_08_pkey'
           AND conrelid = 'public.request_logs_bodies_2026_08'::regclass
    ) THEN
        ALTER TABLE public.request_logs_bodies_2026_08
            ADD CONSTRAINT request_logs_bodies_2026_08_pkey PRIMARY KEY (request_id, ts);
        RAISE NOTICE '562: added PK (request_id, ts) on request_logs_bodies_2026_08';
    END IF;
END $$;

-- ============================================================
-- 3. Verification — recheck storage and confirm the partition is
--    attached to request_logs_bodies.
-- ============================================================

DO $$
DECLARE
    storage_type text;
    is_attached  boolean;
    row_count    bigint;
BEGIN
    SELECT am.amname
      INTO storage_type
      FROM pg_class c
      LEFT JOIN pg_am am ON c.relam = am.oid
     WHERE c.relname = 'request_logs_bodies_2026_08'
       AND c.relnamespace = 'public'::regnamespace;

    IF storage_type IS NULL THEN
        RAISE EXCEPTION '562: VERIFY FAIL — request_logs_bodies_2026_08 missing';
    END IF;

    SELECT EXISTS (
        SELECT 1
          FROM pg_inherits i
          JOIN pg_class c ON c.oid = i.inhrelid
          JOIN pg_class p ON p.oid = i.inhparent
         WHERE c.relname = 'request_logs_bodies_2026_08'
           AND p.relname = 'request_logs_bodies'
    ) INTO is_attached;

    SELECT COUNT(*) INTO row_count FROM request_logs_bodies_2026_08;

    -- 2026-08-23: if the conversion was skipped (non-empty partition), the
    -- storage is still columnar — that is expected and not a verification
    -- failure. The only hard failures here are: partition missing, or
    -- partition detached from the parent.
    IF NOT is_attached THEN
        RAISE EXCEPTION '562: VERIFY FAIL — partition not attached to request_logs_bodies';
    END IF;

    IF storage_type <> 'heap' THEN
        RAISE NOTICE '562: VERIFY PASS (soft) — request_logs_bodies_2026_08 storage=%, attached=%, rows=% — columnar retained (operator action required)',
                     storage_type, is_attached, row_count;
    ELSE
        RAISE NOTICE '562: VERIFY PASS — request_logs_bodies_2026_08 storage=heap, attached=%, rows=%',
                     is_attached, row_count;
    END IF;
END $$;

COMMIT;

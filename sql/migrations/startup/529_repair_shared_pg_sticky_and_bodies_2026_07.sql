-- Migration 529: repair shared-PG sticky conflict key and missing 2026-07 body partition
--
-- The shared 245/154 PostgreSQL ledger records migration 481 as applied, but
-- request_logs_bodies_2026_07 is physically absent. The sticky write path also
-- uses ON CONFLICT (sticky_key) without a matching unique constraint.
--
-- This migration is intentionally fail-closed:
--   - duplicate sticky keys abort the migration; no rows are deleted;
--   - an existing object named request_logs_bodies_2026_07 must be attached to
--     the expected parent with the exact monthly bounds;
--   - all post-conditions are checked before the ledger may record success.

BEGIN;
SET LOCAL TIME ZONE 'Asia/Shanghai';

DO $$
DECLARE
    duplicate_groups BIGINT;
    partition_oid OID;
    partition_bound TEXT;
    expected_bound CONSTANT TEXT :=
        'FOR VALUES FROM (''2026-07-01 00:00:00+08'') TO (''2026-08-01 00:00:00+08'')';
BEGIN
    IF to_regclass('public.sticky_sessions') IS NULL THEN
        RAISE EXCEPTION '529: public.sticky_sessions is missing';
    END IF;

    SELECT count(*)
    INTO duplicate_groups
    FROM (
        SELECT sticky_key
        FROM public.sticky_sessions
        GROUP BY sticky_key
        HAVING count(*) > 1
    ) AS duplicates;

    IF duplicate_groups > 0 THEN
        RAISE EXCEPTION '529: sticky_sessions contains % duplicate sticky_key groups', duplicate_groups;
    END IF;

    IF EXISTS (
        SELECT 1
        FROM pg_constraint
        WHERE conrelid = 'public.sticky_sessions'::regclass
          AND conname = 'uq_sticky_sessions_sticky_key'
          AND contype <> 'u'
    ) THEN
        RAISE EXCEPTION '529: uq_sticky_sessions_sticky_key exists but is not UNIQUE';
    END IF;

    IF NOT EXISTS (
        SELECT 1
        FROM pg_constraint
        WHERE conrelid = 'public.sticky_sessions'::regclass
          AND conname = 'uq_sticky_sessions_sticky_key'
          AND contype = 'u'
    ) THEN
        ALTER TABLE public.sticky_sessions
            ADD CONSTRAINT uq_sticky_sessions_sticky_key UNIQUE (sticky_key);
    END IF;

    partition_oid := to_regclass('public.request_logs_bodies_2026_07');
    IF partition_oid IS NULL THEN
        CREATE TABLE public.request_logs_bodies_2026_07
            PARTITION OF public.request_logs_bodies
            FOR VALUES FROM ('2026-07-01 00:00:00+08')
                         TO ('2026-08-01 00:00:00+08');
        partition_oid := 'public.request_logs_bodies_2026_07'::regclass;
    END IF;

    IF NOT EXISTS (
        SELECT 1
        FROM pg_inherits
        WHERE inhrelid = partition_oid
          AND inhparent = 'public.request_logs_bodies'::regclass
    ) THEN
        RAISE EXCEPTION '529: request_logs_bodies_2026_07 exists but is not attached to request_logs_bodies';
    END IF;

    SELECT pg_get_expr(c.relpartbound, c.oid)
    INTO partition_bound
    FROM pg_class c
    WHERE c.oid = partition_oid;

    IF partition_bound IS DISTINCT FROM expected_bound THEN
        RAISE EXCEPTION '529: request_logs_bodies_2026_07 has unexpected bounds: %', partition_bound;
    END IF;

    IF NOT EXISTS (
        SELECT 1
        FROM pg_index i
        JOIN pg_attribute a
          ON a.attrelid = i.indrelid
         AND a.attnum = i.indkey[0]
        WHERE i.indrelid = 'public.sticky_sessions'::regclass
          AND i.indisunique
          AND i.indnkeyatts = 1
          AND a.attname = 'sticky_key'
    ) THEN
        RAISE EXCEPTION '529: sticky_key unique conflict arbiter was not created';
    END IF;
END $$;

COMMIT;

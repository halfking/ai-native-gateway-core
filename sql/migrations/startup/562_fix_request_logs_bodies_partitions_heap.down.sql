-- Migration 562 (down): Reverse request_logs_bodies_2026_08 heap conversion
--                    and restore ensure_request_logs_bodies_partition to
--                    columnar (NOT RECOMMENDED — columnar blocks the body
--                    promote path; only kept for symmetric rollback under
--                    `bash scripts/sql-rollback.sh 562`).
--
-- Hard constraints from parent task:
--   - DO NOT alter load-bearing partitions (2026_07). The down only touches
--     the empty 2026_08 partition, mirroring the up path.
--   - DO NOT drop the hot table or any trigger. Only partition metadata.
--
-- Behaviour:
--   1. Recreate ensure_request_logs_bodies_partition() with USING columnar
--      (the pre-562 body) and the 328a orphan-reattach ELSIF branch.
--   2. DETACH + DROP + RECREATE request_logs_bodies_2026_08 as columnar.
--      If the partition holds rows (it should not — 562 verified zero rows
--      on the way up), refuse to rollback to avoid data loss.
--   3. Match canonical schema exactly: explicit `+08` bound literal so the
--      rollback is timezone-independent, and full autovacuum storage
--      options so daily autovacuum behavior is preserved.

BEGIN;

DO $$
DECLARE
    storage_type text;
    row_count    bigint;
    is_attached  boolean;
    partition_oid oid;
BEGIN
    SELECT c.oid, am.amname
      INTO partition_oid, storage_type
      FROM pg_class c
      LEFT JOIN pg_am am ON c.relam = am.oid
     WHERE c.relname = 'request_logs_bodies_2026_08'
       AND c.relnamespace = 'public'::regnamespace;

    IF partition_oid IS NULL THEN
        RAISE NOTICE '562 down: request_logs_bodies_2026_08 does not exist — skip conversion';
    ELSE
        SELECT EXISTS (
            SELECT 1
              FROM pg_inherits i
              JOIN pg_class c ON c.oid = i.inhrelid
              JOIN pg_class p ON p.oid = i.inhparent
             WHERE c.oid = partition_oid
               AND p.relname = 'request_logs_bodies'
        ) INTO is_attached;

        SELECT COUNT(*) INTO row_count FROM request_logs_bodies_2026_08;

        RAISE NOTICE '562 down: storage=%, rows=%, attached=%',
                     storage_type, row_count, is_attached;

        IF storage_type = 'columnar' THEN
            RAISE NOTICE '562 down: already columnar — skip';
        ELSE
            IF row_count > 0 THEN
                RAISE EXCEPTION '562 down: ABORT — partition holds % rows; rollback would require migration (not just drop/recreate). Investigate before retrying.', row_count;
            END IF;

            IF NOT is_attached THEN
                RAISE EXCEPTION '562 down: ABORT — partition is detached; manual investigation needed';
            END IF;

            ALTER TABLE public.request_logs_bodies
                DETACH PARTITION public.request_logs_bodies_2026_08;
            RAISE NOTICE '562 down: DETACHED request_logs_bodies_2026_08';

            DROP TABLE public.request_logs_bodies_2026_08 CASCADE;
            RAISE NOTICE '562 down: DROPPED request_logs_bodies_2026_08';

            -- Match canonical schema (sql/schema/01-schema.sql:18401):
            -- explicit +08 bound so the partition range is timezone-stable,
            -- and full autovacuum reloptions so daily maintenance is unchanged.
            CREATE TABLE public.request_logs_bodies_2026_08
                PARTITION OF public.request_logs_bodies
                FOR VALUES FROM ('2026-08-01 00:00:00+08') TO ('2026-09-01 00:00:00+08')
                WITH (autovacuum_enabled='true',
                      autovacuum_vacuum_scale_factor='0.05',
                      autovacuum_vacuum_threshold='10',
                      autovacuum_analyze_scale_factor='0.02',
                      autovacuum_analyze_threshold='50')
                USING columnar;
            RAISE NOTICE '562 down: RECREATED request_logs_bodies_2026_08 as columnar with canonical autovacuum options';

            IF NOT EXISTS (
                SELECT 1 FROM pg_constraint
                 WHERE conname = 'request_logs_bodies_2026_08_pkey'
                   AND conrelid = 'public.request_logs_bodies_2026_08'::regclass
            ) THEN
                ALTER TABLE public.request_logs_bodies_2026_08
                    ADD CONSTRAINT request_logs_bodies_2026_08_pkey PRIMARY KEY (request_id, ts);
                RAISE NOTICE '562 down: added PK (request_id, ts) on request_logs_bodies_2026_08';
            END IF;
        END IF;
    END IF;
END $$;

-- Restore the pre-562 columnar ensure function (NOT RECOMMENDED —
-- columnar bodies blocks the hot→monthly promote path). Body mirrors
-- 328a exactly so the 328a orphan-reattach ELSIF branch is preserved.
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
        EXECUTE format(
            'CREATE TABLE %I PARTITION OF request_logs_bodies
             FOR VALUES FROM (%L) TO (%L) USING columnar',
            partition_name, month_start, month_end
        );
        RAISE NOTICE '562 down: ensure_request_logs_bodies_partition restored to columnar';
    ELSIF NOT EXISTS (
        SELECT 1
        FROM pg_inherits i
        JOIN pg_class c ON c.oid = i.inhrelid
        JOIN pg_class p ON p.oid = i.inhparent
        WHERE c.relname = partition_name
          AND p.relname = 'request_logs_bodies'
    ) THEN
        EXECUTE format(
            'ALTER TABLE request_logs_bodies ATTACH PARTITION %I FOR VALUES FROM (%L) TO (%L)',
            partition_name, month_start, month_end
        );
        RAISE NOTICE '562 down: ensure_request_logs_bodies_partition re-attached orphan %', partition_name;
    END IF;
END;
$$;

COMMIT;

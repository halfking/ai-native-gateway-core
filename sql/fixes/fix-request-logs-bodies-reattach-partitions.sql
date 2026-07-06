-- Re-attach orphaned request_logs_bodies monthly tables to the partitioned parent.
--
-- Root cause:
--   Migration 337 detached current/future request_logs_bodies partitions
--   (2026_07, 2026_08), but no later migration re-attached them.
--   ensure_request_logs_bodies_partition() only checks whether a table name
--   exists; if an orphan table already exists, it skips creation and never
--   restores the ATTACH relationship. Writes then miss the monthly partition
--   and fail unless a DEFAULT partition exists.

BEGIN;

DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM pg_class WHERE relname = 'request_logs_bodies_2026_07'
    ) AND NOT EXISTS (
        SELECT 1
        FROM pg_inherits i
        JOIN pg_class c ON c.oid = i.inhrelid
        JOIN pg_class p ON p.oid = i.inhparent
        WHERE c.relname = 'request_logs_bodies_2026_07'
          AND p.relname = 'request_logs_bodies'
    ) THEN
        ALTER TABLE request_logs_bodies
            ATTACH PARTITION request_logs_bodies_2026_07
            FOR VALUES FROM ('2026-07-01 00:00:00+00') TO ('2026-08-01 00:00:00+00');
        RAISE NOTICE 'Attached request_logs_bodies_2026_07';
    END IF;

    IF EXISTS (
        SELECT 1 FROM pg_class WHERE relname = 'request_logs_bodies_2026_08'
    ) AND NOT EXISTS (
        SELECT 1
        FROM pg_inherits i
        JOIN pg_class c ON c.oid = i.inhrelid
        JOIN pg_class p ON p.oid = i.inhparent
        WHERE c.relname = 'request_logs_bodies_2026_08'
          AND p.relname = 'request_logs_bodies'
    ) THEN
        ALTER TABLE request_logs_bodies
            ATTACH PARTITION request_logs_bodies_2026_08
            FOR VALUES FROM ('2026-08-01 00:00:00+00') TO ('2026-09-01 00:00:00+00');
        RAISE NOTICE 'Attached request_logs_bodies_2026_08';
    END IF;
END $$;

COMMIT;

SELECT c.relname AS child, pg_get_expr(c.relpartbound, c.oid) AS bound
FROM pg_class c
JOIN pg_inherits i ON c.oid = i.inhrelid
WHERE i.inhparent = 'request_logs_bodies'::regclass
ORDER BY c.relname;

-- Verification note:
--   Parent inserts should now route to the correct attached monthly partition.
--   Avoid DELETE / UPDATE verification on columnar child partitions; use a plain
--   INSERT check plus count/read-only inspection instead.

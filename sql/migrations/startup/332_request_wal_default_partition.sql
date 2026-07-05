-- Migration 332: Add request_wal_default partition (and ensure current/next month coverage)
--
-- Background:
--   request_wal is a partitioned table (range on created_at) but does not
--   have a DEFAULT partition. In production, this means an INSERT for a
--   timestamp that does not match any existing monthly partition fails
--   with "no partition of relation \"request_wal\" found for row" (the
--   exact error surfaced in domains/streaming/routing_errors_test.go).
--
-- Per the 2026-07 data-lifecycle architecture:
--   - *_default is the canonical write target (heap storage, supports UPDATE/DELETE)
--   - Monthly columnar/heap partitions are pre-created for current+next month
--   - A background migrator moves rows from *_default into the matching month partition
--   - INSERT/UPDATE/DELETE in Go code must always target *_default explicitly
--
-- This migration:
--   1. Creates the missing request_wal_default partition (catch-all)
--   2. Idempotent: safe to re-run; uses IF NOT EXISTS via pg_class check
--   3. Keeps existing monthly partitions intact (request_wal_2026_06 / _07)

BEGIN;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_class c
        JOIN pg_namespace n ON c.relnamespace = n.oid
        WHERE c.relname = 'request_wal_default'
          AND n.nspname = 'public'
    ) THEN
        CREATE TABLE public.request_wal_default PARTITION OF public.request_wal DEFAULT;
        RAISE NOTICE 'Created partition: request_wal_default';
    ELSE
        RAISE NOTICE 'Partition request_wal_default already exists, skipping';
    END IF;
END $$;

DO $$
DECLARE
    partition_count int;
BEGIN
    SELECT COUNT(*) INTO partition_count
    FROM pg_inherits
    WHERE inhparent = 'public.request_wal'::regclass;

    RAISE NOTICE 'request_wal now has % partitions (default + monthly)', partition_count;
END $$;

COMMIT;

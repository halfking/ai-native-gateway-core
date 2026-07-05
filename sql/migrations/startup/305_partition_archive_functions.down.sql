-- Migration 305 Rollback: Remove partition archive functions for request_wal
--
-- This migration removes:
--   1. archive_request_wal() function
--   2. ensure_request_wal_partition() function
--   3. request_wal_archive table (if empty)
--
-- Date: 2026-06-28

-- Drop functions
DROP FUNCTION IF EXISTS archive_request_wal(date);
DROP FUNCTION IF EXISTS ensure_request_wal_partition(timestamp with time zone);

-- Drop archive table only if it has no partitions with data
-- (Safety check to prevent accidental data loss)
DO $$
DECLARE
    partition_count int;
BEGIN
    -- Count partitions of request_wal_archive
    SELECT COUNT(*) INTO partition_count
    FROM pg_class c
    JOIN pg_inherits i ON c.oid = i.inhrelid
    JOIN pg_class p ON i.inhparent = p.oid
    WHERE p.relname = 'request_wal_archive'
      AND p.relnamespace = 'public'::regnamespace;
    
    IF partition_count = 0 THEN
        -- No partitions, safe to drop
        DROP TABLE IF EXISTS request_wal_archive;
        RAISE NOTICE 'Dropped request_wal_archive table (no partitions found)';
    ELSE
        RAISE WARNING 'request_wal_archive has % partitions, not dropping to prevent data loss', partition_count;
        RAISE NOTICE 'To manually drop: DROP TABLE request_wal_archive CASCADE;';
    END IF;
END $$;

RAISE NOTICE 'Migration 305 rollback completed';

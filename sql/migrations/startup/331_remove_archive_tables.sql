-- Migration 331: Remove archive tables (request_logs_archive, request_wal_archive)
--
-- Background:
--   Archive tables were originally designed to move old data from main
--   partitions to columnar storage for long-term retention. However:
--     - Storage footprint is small (~30 MB/partition for request_logs_archive)
--     - No application code queries archive tables
--     - Adds maintenance overhead (monthly archive jobs, cron scripts)
--     - Data retention can be handled via regular database backups
--
-- Decision:
--   Remove archive tables and archive functions to simplify architecture.
--   Main table partitions (request_logs_2026_XX, request_wal_2026_XX) will
--   be retained longer or dropped directly when no longer needed.
--
-- What this migration does:
--   1. Drop archive functions: archive_request_logs(), archive_request_wal()
--   2. Drop archive parent tables: request_logs_archive, request_wal_archive
--   3. All child partitions (request_logs_archive_2026_XX) are dropped CASCADE
--
-- Data loss:
--   ⚠️  Any data in archive tables (2026_06, 2026_07) will be PERMANENTLY DELETED
--   ⚠️  Make sure to backup archive tables first if needed:
--        pg_dump -t 'request_logs_archive*' -t 'request_wal_archive*' > archives_backup.sql
--
-- Post-migration:
--   - bg/partition_manager.go must be updated to remove archive specs
--   - scripts/columnar-monthly-cron.sh must be updated
--   - admin/data_lifecycle_partition.go must be updated
--
-- Rollback:
--   Apply 331_remove_archive_tables.down.sql to restore archive infrastructure
--   (but archived data will NOT be restored unless you restore from backup)
--
-- Added: 2026-07-04
-- Author: ACC team

BEGIN;

-- Step 1: Backup check reminder
DO $$
BEGIN
    RAISE NOTICE '⚠️  WARNING: This migration will PERMANENTLY DELETE all archive tables and data.';
    RAISE NOTICE '⚠️  If you need to preserve archived data, cancel this migration and run:';
    RAISE NOTICE '    pg_dump -t "request_logs_archive*" -t "request_wal_archive*" > archives_backup.sql';
    RAISE NOTICE '';
    RAISE NOTICE 'Proceeding with archive table removal in 5 seconds...';
    PERFORM pg_sleep(5);
END $$;

-- Step 2: Drop archive functions
DO $$
BEGIN
    -- Drop request_logs archive function
    IF EXISTS (
        SELECT 1 FROM pg_proc p
        JOIN pg_namespace n ON p.pronamespace = n.oid
        WHERE n.nspname = 'public' AND p.proname = 'archive_request_logs'
    ) THEN
        DROP FUNCTION public.archive_request_logs(date);
        RAISE NOTICE 'Dropped function: archive_request_logs(date)';
    ELSE
        RAISE NOTICE 'Function archive_request_logs(date) does not exist, skipping';
    END IF;

    -- Drop request_wal archive function
    IF EXISTS (
        SELECT 1 FROM pg_proc p
        JOIN pg_namespace n ON p.pronamespace = n.oid
        WHERE n.nspname = 'public' AND p.proname = 'archive_request_wal'
    ) THEN
        DROP FUNCTION public.archive_request_wal(date);
        RAISE NOTICE 'Dropped function: archive_request_wal(date)';
    ELSE
        RAISE NOTICE 'Function archive_request_wal(date) does not exist, skipping';
    END IF;
END $$;

-- Step 3: Drop routing_decision_log and credential_model_index archive functions
-- (added for completeness, as they were also part of the archive system)
DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM pg_proc p
        JOIN pg_namespace n ON p.pronamespace = n.oid
        WHERE n.nspname = 'public' AND p.proname = 'archive_routing_decision_log'
    ) THEN
        DROP FUNCTION public.archive_routing_decision_log(date);
        RAISE NOTICE 'Dropped function: archive_routing_decision_log(date)';
    END IF;

    IF EXISTS (
        SELECT 1 FROM pg_proc p
        JOIN pg_namespace n ON p.pronamespace = n.oid
        WHERE n.nspname = 'public' AND p.proname = 'archive_credential_model_index'
    ) THEN
        DROP FUNCTION public.archive_credential_model_index(date);
        RAISE NOTICE 'Dropped function: archive_credential_model_index(date)';
    END IF;
END $$;

-- Step 4: List existing archive partitions (for logging)
DO $$
DECLARE
    partition_record RECORD;
    total_size bigint := 0;
BEGIN
    RAISE NOTICE 'Archive partitions to be deleted:';
    FOR partition_record IN
        SELECT tablename, pg_size_pretty(pg_total_relation_size('public.' || tablename)) as size
        FROM pg_tables
        WHERE schemaname = 'public'
          AND (tablename LIKE 'request_logs_archive_%'
               OR tablename LIKE 'request_wal_archive_%'
               OR tablename LIKE 'routing_decision_log_archive_%'
               OR tablename LIKE 'credential_model_index_archive_%')
    LOOP
        RAISE NOTICE '  - % (size: %)', partition_record.tablename, partition_record.size;
    END LOOP;
END $$;

-- Step 5: Drop archive parent tables (CASCADE drops all child partitions)
DO $$
BEGIN
    -- Drop request_logs_archive
    IF EXISTS (
        SELECT 1 FROM pg_class c
        JOIN pg_namespace n ON c.relnamespace = n.oid
        WHERE n.nspname = 'public' AND c.relname = 'request_logs_archive'
    ) THEN
        DROP TABLE public.request_logs_archive CASCADE;
        RAISE NOTICE 'Dropped table: request_logs_archive (and all child partitions)';
    ELSE
        RAISE NOTICE 'Table request_logs_archive does not exist, skipping';
    END IF;

    -- Drop request_wal_archive
    IF EXISTS (
        SELECT 1 FROM pg_class c
        JOIN pg_namespace n ON c.relnamespace = n.oid
        WHERE n.nspname = 'public' AND c.relname = 'request_wal_archive'
    ) THEN
        DROP TABLE public.request_wal_archive CASCADE;
        RAISE NOTICE 'Dropped table: request_wal_archive (and all child partitions)';
    ELSE
        RAISE NOTICE 'Table request_wal_archive does not exist, skipping';
    END IF;

    -- Drop routing_decision_log_archive
    IF EXISTS (
        SELECT 1 FROM pg_class c
        JOIN pg_namespace n ON c.relnamespace = n.oid
        WHERE n.nspname = 'public' AND c.relname = 'routing_decision_log_archive'
    ) THEN
        DROP TABLE public.routing_decision_log_archive CASCADE;
        RAISE NOTICE 'Dropped table: routing_decision_log_archive (and all child partitions)';
    END IF;

    -- Drop credential_model_index_archive
    IF EXISTS (
        SELECT 1 FROM pg_class c
        JOIN pg_namespace n ON c.relnamespace = n.oid
        WHERE n.nspname = 'public' AND c.relname = 'credential_model_index_archive'
    ) THEN
        DROP TABLE public.credential_model_index_archive CASCADE;
        RAISE NOTICE 'Dropped table: credential_model_index_archive (and all child partitions)';
    END IF;
END $$;

-- Step 6: Verify removal
DO $$
DECLARE
    remaining_count int;
BEGIN
    SELECT COUNT(*) INTO remaining_count
    FROM pg_tables
    WHERE schemaname = 'public'
      AND tablename LIKE '%_archive%';
    
    IF remaining_count > 0 THEN
        RAISE WARNING 'Still found % archive tables after cleanup', remaining_count;
    ELSE
        RAISE NOTICE '✓ All archive tables successfully removed';
    END IF;
END $$;

COMMIT;

-- Post-migration checklist:
--   [ ] Update bg/partition_manager.go - remove archiveSpecs entries
--   [ ] Update scripts/columnar-monthly-cron.sh - remove archive commands
--   [ ] Update admin/data_lifecycle_partition.go - remove archive table configs
--   [ ] Update docs/DATA_LIFECYCLE_PARTITION_README.md - document removal
--   [ ] Establish new policy for old partition cleanup (manual or automated)
--   [ ] Consider implementing export-to-S3 for long-term retention if needed

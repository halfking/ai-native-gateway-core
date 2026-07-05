-- Migration 331: Rollback - Restore archive tables infrastructure
--
-- This rollback script restores the archive functions and parent tables,
-- but does NOT restore the archived data. If you backed up archive data
-- before applying 331_remove_archive_tables.sql, restore it separately:
--
--   psql -U llm_gateway -d llm_gateway < archives_backup.sql
--
-- Note: This rollback requires re-applying migrations 305, 318, and 318b
-- which originally created the archive infrastructure.

BEGIN;

RAISE NOTICE 'Rollback: Restoring archive table infrastructure';
RAISE NOTICE 'WARNING: This only restores table structure, not archived data';
RAISE NOTICE 'To restore data, you must separately restore from backup';

-- Restore archive functions and tables by re-applying original migrations
-- User should manually run:
--   psql < db/migrations/305_partition_archive_functions.sql
--   psql < db/migrations/318_fix_archive_functions.sql
--   psql < db/migrations/318b_request_logs_archive_heap.sql

RAISE EXCEPTION 'Manual rollback required: Apply migrations 305, 318, 318b to restore archive infrastructure';

COMMIT;

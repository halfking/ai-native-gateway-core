-- 392_candidate_failure_logs_monthly_partition.down.sql
-- Reverses the candidate_failure_logs hot + monthly partition migration.
-- Restores the original columnar heap table layout.

BEGIN;

-- 1. Drop the view
DROP VIEW IF EXISTS candidate_failure_logs_with_current_month;

-- 2. Drop RLS policy
DROP POLICY IF EXISTS tenant_isolation_candidate_failure_logs_hot ON candidate_failure_logs_hot;

-- 3. Detach all monthly partitions and drop the partitioned parent
DO $$
DECLARE
    r RECORD;
BEGIN
    FOR r IN
        SELECT c.relname
        FROM pg_inherits i
        JOIN pg_class p ON p.oid = i.inhparent
        JOIN pg_class c ON c.oid = i.inhrelid
        WHERE p.relname = 'candidate_failure_logs'
    LOOP
        EXECUTE format('ALTER TABLE candidate_failure_logs DETACH PARTITION %I', r.relname);
    END LOOP;
END $$;

DROP TABLE IF EXISTS candidate_failure_logs;

-- 4. Restore original columnar heap table from archive (if exists)
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_class WHERE relname = 'candidate_failure_logs_columnar_archive') THEN
        ALTER TABLE candidate_failure_logs_columnar_archive RENAME TO candidate_failure_logs;
        RAISE NOTICE 'Restored original columnar candidate_failure_logs from archive';
    ELSE
        RAISE NOTICE 'No archive found; original data may have been migrated to hot + partitions';
    END IF;
END $$;

-- 5. Drop the hot table
DROP TABLE IF EXISTS candidate_failure_logs_hot;

-- 6. Drop helper functions
DROP FUNCTION IF EXISTS promote_candidate_failure_logs_hot_to_partition(interval, int);
DROP FUNCTION IF EXISTS ensure_candidate_failure_logs_partition(timestamp with time zone);

COMMIT;

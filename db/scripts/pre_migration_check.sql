-- Pre-Migration Check for Request Logs Fix
-- Date: 2026-06-26
-- Purpose: Analyze current state before applying migration 301

-- ============================================================================
-- SECTION 1: Check Current Index Structure
-- ============================================================================

SELECT 
    'Current Indexes on request_logs' as check_type,
    indexname as index_name,
    indexdef as definition
FROM pg_indexes
WHERE tablename = 'request_logs'
  AND indexname LIKE '%request_id%'
ORDER BY indexname;

-- ============================================================================
-- SECTION 2: Check for Duplicate Records
-- ============================================================================

-- Overall duplicate count (last 7 days)
SELECT 
    'Duplicate Analysis (last 7 days)' as check_type,
    COUNT(DISTINCT request_id) as unique_requests,
    COUNT(*) as total_rows,
    COUNT(*) - COUNT(DISTINCT request_id) as duplicate_rows,
    ROUND(100.0 * (COUNT(*) - COUNT(DISTINCT request_id)) / NULLIF(COUNT(*), 0), 2) as duplicate_percentage
FROM request_logs
WHERE ts > now() - interval '7 days';

-- Overall duplicate count (all time)
SELECT 
    'Duplicate Analysis (all time)' as check_type,
    COUNT(DISTINCT request_id) as unique_requests,
    COUNT(*) as total_rows,
    COUNT(*) - COUNT(DISTINCT request_id) as duplicate_rows
FROM request_logs;

-- ============================================================================
-- SECTION 3: Check Status Distribution
-- ============================================================================

SELECT 
    'Status Distribution (last 7 days)' as check_type,
    request_status,
    success,
    COUNT(*) as count,
    ROUND(100.0 * COUNT(*) / SUM(COUNT(*)) OVER (), 2) as percentage
FROM request_logs
WHERE ts > now() - interval '7 days'
GROUP BY request_status, success
ORDER BY count DESC;

-- ============================================================================
-- SECTION 4: Check 'in_progress' Records
-- ============================================================================

SELECT 
    'In-Progress Records Analysis' as check_type,
    COUNT(*) as total_in_progress,
    COUNT(DISTINCT request_id) as unique_requests,
    COUNT(*) - COUNT(DISTINCT request_id) as duplicate_in_progress
FROM request_logs
WHERE request_status = 'in_progress'
  AND ts > now() - interval '7 days';

-- Find the worst duplicate cases
SELECT 
    'Worst Duplicate Cases (last 7 days)' as check_type,
    request_id,
    COUNT(*) as duplicate_count,
    MIN(ts) as first_created,
    MAX(ts) as last_created,
    MAX(ts) - MIN(ts) as time_span
FROM request_logs
WHERE ts > now() - interval '7 days'
GROUP BY request_id
HAVING COUNT(*) > 1
ORDER BY COUNT(*) DESC
LIMIT 10;

-- ============================================================================
-- SECTION 5: Migration Necessity Assessment
-- ============================================================================

SELECT 
    '=== MIGRATION NECESSITY ASSESSMENT ===' as result;

-- Assessment query
DO $$
DECLARE
    dup_count INTEGER;
    dup_count_all INTEGER;
    migration_needed BOOLEAN := FALSE;
    migration_benefit TEXT := 'LOW';
BEGIN
    -- Check duplicates in last 7 days
    SELECT COUNT(*) - COUNT(DISTINCT request_id)
    INTO dup_count
    FROM request_logs
    WHERE ts > now() - interval '7 days';
    
    -- Check duplicates all time
    SELECT COUNT(*) - COUNT(DISTINCT request_id)
    INTO dup_count_all
    FROM request_logs;
    
    RAISE NOTICE '';
    RAISE NOTICE '============================================';
    RAISE NOTICE '     MIGRATION PRE-CHECK RESULTS';
    RAISE NOTICE '============================================';
    RAISE NOTICE '';
    RAISE NOTICE 'Duplicate records (last 7 days): %', dup_count;
    RAISE NOTICE 'Duplicate records (all time): %', dup_count_all;
    RAISE NOTICE '';
    
    -- Determine if migration is needed
    IF dup_count > 0 OR dup_count_all > 0 THEN
        migration_needed := TRUE;
        
        IF dup_count > 100 OR dup_count_all > 1000 THEN
            migration_benefit := 'HIGH';
        ELSIF dup_count > 10 OR dup_count_all > 100 THEN
            migration_benefit := 'MEDIUM';
        ELSE
            migration_benefit := 'LOW';
        END IF;
    END IF;
    
    RAISE NOTICE 'Migration Recommended: %', migration_needed;
    RAISE NOTICE 'Migration Benefit: %', migration_benefit;
    RAISE NOTICE '';
    
    IF migration_needed THEN
        IF migration_benefit = 'HIGH' THEN
            RAISE NOTICE '>>> MIGRATION IS STRONGLY RECOMMENDED <<<';
            RAISE NOTICE '>>> There are significant duplicates that need cleanup <<<';
        ELSIF migration_benefit = 'MEDIUM' THEN
            RAISE NOTICE '>>> MIGRATION IS RECOMMENDED <<<';
            RAISE NOTICE '>>> Some duplicates exist and should be cleaned up <<<';
        ELSE
            RAISE NOTICE '>>> MIGRATION IS OPTIONAL <<<';
            RAISE NOTICE '>>> Few duplicates exist; code fix is sufficient <<<';
        END IF;
    ELSE
        RAISE NOTICE '>>> MIGRATION MAY NOT BE NEEDED <<<';
        RAISE NOTICE '>>> Code fix (ON CONFLICT request_id) already prevents new duplicates <<<';
    END IF;
    
    RAISE NOTICE '';
    RAISE NOTICE '============================================';
END $$;

-- ============================================================================
-- SECTION 6: Index Existence Check
-- ============================================================================

SELECT 
    'Index Existence Check' as check_type,
    CASE 
        WHEN EXISTS (
            SELECT 1 FROM pg_indexes 
            WHERE tablename = 'request_logs' 
              AND indexname = 'idx_request_logs_request_id_ts_unique'
        ) THEN 'EXISTS - will be dropped'
        WHEN EXISTS (
            SELECT 1 FROM pg_indexes 
            WHERE tablename = 'request_logs' 
              AND indexname = 'idx_request_logs_request_id_unique'
        ) THEN 'EXISTS - already updated'
        ELSE 'DOES NOT EXIST'
    END as idx_request_id_ts_status,
    CASE 
        WHEN EXISTS (
            SELECT 1 FROM pg_indexes 
            WHERE tablename = 'request_logs' 
              AND indexname = 'idx_request_logs_request_id_unique'
        ) THEN 'EXISTS - new index already present'
        ELSE 'DOES NOT EXIST - will be created'
    END as idx_request_id_only_status;

-- ============================================================================
-- SECTION 7: Table Size (for lock estimation)
-- ============================================================================

SELECT 
    'Table Size Analysis' as check_type,
    pg_size_pretty(pg_total_relation_size('request_logs')) as total_size,
    pg_size_pretty(pg_relation_size('request_logs')) as table_size,
    pg_size_pretty(pg_indexes_size('request_logs')) as index_size,
    (SELECT COUNT(*) FROM request_logs) as row_count;

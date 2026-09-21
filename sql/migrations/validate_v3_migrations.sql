-- AUTO_MODEL V3 - Schema Validation Queries
-- Purpose: Verify migration success and data integrity
-- Date: 2026-09-02

-- ============================================================================
-- 1. Verify New Columns in request_logs
-- ============================================================================

\echo '=== 1. Checking request_logs schema ==='

SELECT 
    column_name,
    data_type,
    is_nullable,
    column_default
FROM information_schema.columns
WHERE table_name = 'request_logs'
  AND column_name IN (
    'request_type', 'parent_request_id', 'request_depth', 
    'is_terminal', 'task_tier', 'auto_decision', 'escalation_count'
  )
ORDER BY column_name;

-- Expected: 7 rows with all new columns

-- ============================================================================
-- 2. Verify Indexes Created
-- ============================================================================

\echo ''
\echo '=== 2. Checking indexes on request_logs ==='

SELECT 
    indexname,
    indexdef
FROM pg_indexes
WHERE tablename = 'request_logs'
  AND indexname LIKE 'idx_request_logs_%'
  AND indexname IN (
    'idx_request_logs_request_type_ts',
    'idx_request_logs_parent_request_id',
    'idx_request_logs_task_tier_ts',
    'idx_request_logs_terminal',
    'idx_request_logs_tier_terminal_ts',
    'idx_request_logs_escalation'
  )
ORDER BY indexname;

-- Expected: 6 indexes

-- ============================================================================
-- 3. Verify task_type_tier_config Table
-- ============================================================================

\echo ''
\echo '=== 3. Checking task_type_tier_config table ==='

SELECT 
    task_type,
    preferred_tier,
    fallback_tiers,
    min_confidence,
    enabled,
    description
FROM task_type_tier_config
WHERE tenant_id IS NULL
ORDER BY 
    CASE preferred_tier
        WHEN 'tier-a' THEN 1
        WHEN 'tier-b' THEN 2
        WHEN 'tier-c' THEN 3
    END,
    task_type;

-- Expected: 10 rows (3 tier-a, 3 tier-b, 4 tier-c)

-- ============================================================================
-- 4. Verify Tier Distribution in Task Config
-- ============================================================================

\echo ''
\echo '=== 4. Task type tier distribution ==='

SELECT 
    preferred_tier,
    COUNT(*) as task_count,
    ARRAY_AGG(task_type ORDER BY task_type) as tasks
FROM task_type_tier_config
WHERE tenant_id IS NULL AND enabled = TRUE
GROUP BY preferred_tier
ORDER BY preferred_tier;

-- Expected:
-- tier-a: 3 (architecture, audit, debugging)
-- tier-b: 3 (coding, refactoring, testing)
-- tier-c: 4 (dependency, devops, documentation, summary)

-- ============================================================================
-- 5. Verify provider_models Tier Classification
-- ============================================================================

\echo ''
\echo '=== 5. Checking provider_models tier classification ==='

SELECT 
    tier,
    COUNT(*) as model_count,
    COUNT(*) FILTER (WHERE available = TRUE) as available_count
FROM provider_models
GROUP BY tier
ORDER BY tier NULLS LAST;

-- Expected: tier-a, tier-b, tier-c with counts

-- ============================================================================
-- 6. Sample Tier-Classified Models
-- ============================================================================

\echo ''
\echo '=== 6. Sample models by tier ==='

SELECT 
    tier,
    canonical_name,
    available,
    COALESCE(unit_price_in, 0) as price_per_1m_in
FROM provider_models
WHERE tier IS NOT NULL
ORDER BY tier, price_per_1m_in DESC
LIMIT 20;

-- Expected: Mix of models across tiers

-- ============================================================================
-- 7. Check for Unclassified Available Models
-- ============================================================================

\echo ''
\echo '=== 7. Unclassified available models (should be empty) ==='

SELECT 
    canonical_name,
    provider_name,
    available,
    unit_price_in
FROM provider_models
WHERE tier IS NULL 
  AND available = TRUE
ORDER BY canonical_name;

-- Expected: 0 rows (all available models should have tier)

-- ============================================================================
-- 8. Verify request_logs Data Backfill
-- ============================================================================

\echo ''
\echo '=== 8. Request logs backfill verification ==='

SELECT 
    request_type,
    is_terminal,
    COUNT(*) as count
FROM request_logs
GROUP BY request_type, is_terminal
ORDER BY request_type, is_terminal;

-- Expected: Most existing records marked as 'outbound' with is_terminal=true

-- ============================================================================
-- 9. Test Query Performance (EXPLAIN ANALYZE)
-- ============================================================================

\echo ''
\echo '=== 9. Query performance test ==='

-- Query 1: Client requests in last 24 hours (new pattern)
EXPLAIN ANALYZE
SELECT COUNT(*) 
FROM request_logs 
WHERE request_type = 'client' 
  AND ts >= NOW() - INTERVAL '1 day';

\echo ''

-- Query 2: Cost per tier (new pattern)
EXPLAIN ANALYZE
SELECT 
    task_tier,
    SUM(total_tokens) as total_tokens
FROM request_logs
WHERE is_terminal = TRUE 
  AND ts >= NOW() - INTERVAL '1 day'
  AND task_tier IS NOT NULL
GROUP BY task_tier;

\echo ''

-- Query 3: Parent-child relationship (new pattern)
EXPLAIN ANALYZE
SELECT 
    parent_request_id,
    COUNT(*) as attempts
FROM request_logs
WHERE request_type = 'outbound'
  AND parent_request_id IS NOT NULL
  AND ts >= NOW() - INTERVAL '1 day'
GROUP BY parent_request_id
LIMIT 10;

-- ============================================================================
-- 10. Summary Report
-- ============================================================================

\echo ''
\echo '=== 10. Migration Summary ==='

DO $$
DECLARE
    v_request_log_columns INTEGER;
    v_request_log_indexes INTEGER;
    v_task_config_rows INTEGER;
    v_tier_a_count INTEGER;
    v_tier_b_count INTEGER;
    v_tier_c_count INTEGER;
    v_unclassified_count INTEGER;
BEGIN
    -- Count new columns
    SELECT COUNT(*) INTO v_request_log_columns
    FROM information_schema.columns
    WHERE table_name = 'request_logs'
      AND column_name IN ('request_type', 'parent_request_id', 'request_depth', 
                          'is_terminal', 'task_tier', 'auto_decision', 'escalation_count');
    
    -- Count new indexes
    SELECT COUNT(*) INTO v_request_log_indexes
    FROM pg_indexes
    WHERE tablename = 'request_logs'
      AND indexname IN (
        'idx_request_logs_request_type_ts',
        'idx_request_logs_parent_request_id',
        'idx_request_logs_task_tier_ts',
        'idx_request_logs_terminal',
        'idx_request_logs_tier_terminal_ts',
        'idx_request_logs_escalation'
      );
    
    -- Count task type configs
    SELECT COUNT(*) INTO v_task_config_rows
    FROM task_type_tier_config
    WHERE tenant_id IS NULL;
    
    -- Count models by tier
    SELECT 
        COUNT(*) FILTER (WHERE tier = 'tier-a'),
        COUNT(*) FILTER (WHERE tier = 'tier-b'),
        COUNT(*) FILTER (WHERE tier = 'tier-c'),
        COUNT(*) FILTER (WHERE tier IS NULL AND available = TRUE)
    INTO v_tier_a_count, v_tier_b_count, v_tier_c_count, v_unclassified_count
    FROM provider_models;
    
    RAISE NOTICE '';
    RAISE NOTICE '╔════════════════════════════════════════════════════════════╗';
    RAISE NOTICE '║         AUTO_MODEL V3 Migration Summary                    ║';
    RAISE NOTICE '╠════════════════════════════════════════════════════════════╣';
    RAISE NOTICE '║ request_logs new columns:        % / 7 expected          ║', LPAD(v_request_log_columns::TEXT, 2);
    RAISE NOTICE '║ request_logs new indexes:        % / 6 expected          ║', LPAD(v_request_log_indexes::TEXT, 2);
    RAISE NOTICE '║ task_type_tier_config rows:      % / 10 expected         ║', LPAD(v_task_config_rows::TEXT, 2);
    RAISE NOTICE '║                                                            ║';
    RAISE NOTICE '║ Provider Models Tier Distribution:                         ║';
    RAISE NOTICE '║   - Tier-A (high-perf):          % models                ║', LPAD(v_tier_a_count::TEXT, 2);
    RAISE NOTICE '║   - Tier-B (standard):           % models                ║', LPAD(v_tier_b_count::TEXT, 2);
    RAISE NOTICE '║   - Tier-C (economy):            % models                ║', LPAD(v_tier_c_count::TEXT, 2);
    RAISE NOTICE '║   - Unclassified (available):    % models                ║', LPAD(v_unclassified_count::TEXT, 2);
    RAISE NOTICE '╠════════════════════════════════════════════════════════════╣';
    
    IF v_request_log_columns = 7 
       AND v_request_log_indexes = 6 
       AND v_task_config_rows = 10 
       AND v_unclassified_count = 0 THEN
        RAISE NOTICE '║ Status: ✓ ALL CHECKS PASSED                               ║';
    ELSE
        RAISE NOTICE '║ Status: ⚠ SOME CHECKS FAILED - Review output above        ║';
    END IF;
    
    RAISE NOTICE '╚════════════════════════════════════════════════════════════╝';
    RAISE NOTICE '';
END $$;

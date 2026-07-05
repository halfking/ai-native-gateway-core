-- Partition Hot Table Architecture Integration Tests
-- 分区表热表架构集成测试
--
-- Purpose: 验证所有分区表的hot表架构正确实现
-- Author: llm-gateway-ops (2026-07-05)
--
-- 运行方式:
--   psql -h localhost -U postgres -d llm_gateway -f partition_hot_table_tests.sql

\echo '=================================='
\echo 'Partition Hot Table Architecture Tests'
\echo 'Started at: ' :`date`
\echo '=================================='
\echo ''

BEGIN;

-- ============================================================
-- 测试准备：创建临时测试数据
-- ============================================================

\echo '1. Preparing test data...'

DO $$
DECLARE
  test_request_id text := 'test_' || extract(epoch from now())::bigint::text;
  test_tenant_id text := 'test_tenant';
  test_tool_id text := 'test_tool';
BEGIN
  -- 存储测试ID供后续使用
  CREATE TEMP TABLE test_ids (
    request_id text,
    tenant_id text,
    tool_id text
  );
  
  INSERT INTO test_ids VALUES (test_request_id, test_tenant_id, test_tool_id);
  
  RAISE NOTICE 'Test IDs created: request_id=%, tenant_id=%, tool_id=%', 
    test_request_id, test_tenant_id, test_tool_id;
END $$;

-- ============================================================
-- 测试 1: request_logs_hot
-- ============================================================

\echo ''
\echo '=================================='
\echo 'Test 1: request_logs_hot'
\echo '=================================='

-- 1.1 测试 INSERT
\echo '1.1 Testing INSERT into request_logs_hot...'
DO $$
DECLARE
  test_req_id text;
  test_tenant text;
  inserted_count int;
BEGIN
  SELECT request_id, tenant_id INTO test_req_id, test_tenant FROM test_ids;
  
  INSERT INTO request_logs_hot (
    request_id, tenant_id, ts, success, model
  ) VALUES (
    test_req_id, test_tenant, now(), true, 'test-model'
  );
  
  SELECT count(*) INTO inserted_count 
  FROM request_logs_hot 
  WHERE request_id = test_req_id;
  
  IF inserted_count = 1 THEN
    RAISE NOTICE '✅ INSERT into request_logs_hot: PASSED';
  ELSE
    RAISE EXCEPTION '❌ INSERT into request_logs_hot: FAILED (count=%)', inserted_count;
  END IF;
END $$;

-- 1.2 测试 UPDATE
\echo '1.2 Testing UPDATE on request_logs_hot...'
DO $$
DECLARE
  test_req_id text;
  updated_success boolean;
BEGIN
  SELECT request_id INTO test_req_id FROM test_ids;
  
  UPDATE request_logs_hot 
  SET success = false, error_class = 'test_error'
  WHERE request_id = test_req_id;
  
  SELECT success INTO updated_success 
  FROM request_logs_hot 
  WHERE request_id = test_req_id;
  
  IF updated_success = false THEN
    RAISE NOTICE '✅ UPDATE on request_logs_hot: PASSED';
  ELSE
    RAISE EXCEPTION '❌ UPDATE on request_logs_hot: FAILED';
  END IF;
END $$;

-- 1.3 测试 VIEW 查询
\echo '1.3 Testing SELECT from request_logs_with_current_month...'
DO $$
DECLARE
  test_req_id text;
  view_count int;
BEGIN
  SELECT request_id INTO test_req_id FROM test_ids;
  
  SELECT count(*) INTO view_count 
  FROM request_logs_with_current_month 
  WHERE request_id = test_req_id;
  
  IF view_count = 1 THEN
    RAISE NOTICE '✅ SELECT from request_logs_with_current_month: PASSED';
  ELSE
    RAISE EXCEPTION '❌ SELECT from request_logs_with_current_month: FAILED (count=%)', view_count;
  END IF;
END $$;

-- 1.4 测试 DELETE
\echo '1.4 Testing DELETE from request_logs_hot...'
DO $$
DECLARE
  test_req_id text;
  remaining_count int;
BEGIN
  SELECT request_id INTO test_req_id FROM test_ids;
  
  DELETE FROM request_logs_hot WHERE request_id = test_req_id;
  
  SELECT count(*) INTO remaining_count 
  FROM request_logs_hot 
  WHERE request_id = test_req_id;
  
  IF remaining_count = 0 THEN
    RAISE NOTICE '✅ DELETE from request_logs_hot: PASSED';
  ELSE
    RAISE EXCEPTION '❌ DELETE from request_logs_hot: FAILED (remaining=%)', remaining_count;
  END IF;
END $$;

-- ============================================================
-- 测试 2: usage_ledger_hot
-- ============================================================

\echo ''
\echo '=================================='
\echo 'Test 2: usage_ledger_hot'
\echo '=================================='

-- 2.1 测试 INSERT
\echo '2.1 Testing INSERT into usage_ledger_hot...'
DO $$
DECLARE
  test_req_id text;
  test_tenant text;
  inserted_count int;
BEGIN
  SELECT request_id, tenant_id INTO test_req_id, test_tenant FROM test_ids;
  
  INSERT INTO usage_ledger_hot (
    request_id, tenant_id, ts, model, prompt_tokens, completion_tokens, cost_usd
  ) VALUES (
    test_req_id, test_tenant, now(), 'test-model', 100, 50, 0.001
  );
  
  SELECT count(*) INTO inserted_count 
  FROM usage_ledger_hot 
  WHERE request_id = test_req_id;
  
  IF inserted_count = 1 THEN
    RAISE NOTICE '✅ INSERT into usage_ledger_hot: PASSED';
  ELSE
    RAISE EXCEPTION '❌ INSERT into usage_ledger_hot: FAILED (count=%)', inserted_count;
  END IF;
END $$;

-- 2.2 测试 UPDATE
\echo '2.2 Testing UPDATE on usage_ledger_hot...'
DO $$
DECLARE
  test_req_id text;
  updated_cost numeric;
BEGIN
  SELECT request_id INTO test_req_id FROM test_ids;
  
  UPDATE usage_ledger_hot 
  SET cost_usd = 0.002
  WHERE request_id = test_req_id;
  
  SELECT cost_usd INTO updated_cost 
  FROM usage_ledger_hot 
  WHERE request_id = test_req_id;
  
  IF updated_cost = 0.002 THEN
    RAISE NOTICE '✅ UPDATE on usage_ledger_hot: PASSED';
  ELSE
    RAISE EXCEPTION '❌ UPDATE on usage_ledger_hot: FAILED (cost=%)', updated_cost;
  END IF;
END $$;

-- 2.3 测试 VIEW 查询
\echo '2.3 Testing SELECT from usage_ledger_with_current_month...'
DO $$
DECLARE
  test_req_id text;
  view_count int;
BEGIN
  SELECT request_id INTO test_req_id FROM test_ids;
  
  SELECT count(*) INTO view_count 
  FROM usage_ledger_with_current_month 
  WHERE request_id = test_req_id;
  
  IF view_count = 1 THEN
    RAISE NOTICE '✅ SELECT from usage_ledger_with_current_month: PASSED';
  ELSE
    RAISE EXCEPTION '❌ SELECT from usage_ledger_with_current_month: FAILED (count=%)', view_count;
  END IF;
END $$;

-- 2.4 清理
\echo '2.4 Cleaning up usage_ledger_hot test data...'
DELETE FROM usage_ledger_hot WHERE request_id IN (SELECT request_id FROM test_ids);

-- ============================================================
-- 测试 3: credit_ledger_hot
-- ============================================================

\echo ''
\echo '=================================='
\echo 'Test 3: credit_ledger_hot'
\echo '=================================='

-- 3.1 测试 INSERT
\echo '3.1 Testing INSERT into credit_ledger_hot...'
DO $$
DECLARE
  test_tenant text;
  test_id bigint;
  inserted_count int;
BEGIN
  SELECT tenant_id INTO test_tenant FROM test_ids;
  
  INSERT INTO credit_ledger_hot (
    tenant_id, entry_type, amount, balance_after, note, created_at
  ) VALUES (
    test_tenant, 'topup', 1000, 1000, 'test topup', now()
  ) RETURNING id INTO test_id;
  
  -- 保存test_id供后续使用
  CREATE TEMP TABLE IF NOT EXISTS test_credit_ledger_id (id bigint);
  INSERT INTO test_credit_ledger_id VALUES (test_id);
  
  SELECT count(*) INTO inserted_count 
  FROM credit_ledger_hot 
  WHERE id = test_id;
  
  IF inserted_count = 1 THEN
    RAISE NOTICE '✅ INSERT into credit_ledger_hot: PASSED (id=%)', test_id;
  ELSE
    RAISE EXCEPTION '❌ INSERT into credit_ledger_hot: FAILED';
  END IF;
END $$;

-- 3.2 测试 VIEW 查询
\echo '3.2 Testing SELECT from credit_ledger_with_current_month...'
DO $$
DECLARE
  test_id bigint;
  view_count int;
BEGIN
  SELECT id INTO test_id FROM test_credit_ledger_id;
  
  SELECT count(*) INTO view_count 
  FROM credit_ledger_with_current_month 
  WHERE id = test_id;
  
  IF view_count = 1 THEN
    RAISE NOTICE '✅ SELECT from credit_ledger_with_current_month: PASSED';
  ELSE
    RAISE EXCEPTION '❌ SELECT from credit_ledger_with_current_month: FAILED';
  END IF;
END $$;

-- 3.3 清理
\echo '3.3 Cleaning up credit_ledger_hot test data...'
DELETE FROM credit_ledger_hot WHERE id IN (SELECT id FROM test_credit_ledger_id);

-- ============================================================
-- 测试 4: tool_usage_stats_hot
-- ============================================================

\echo ''
\echo '=================================='
\echo 'Test 4: tool_usage_stats_hot'
\echo '=================================='

-- 4.1 测试 INSERT (UPSERT)
\echo '4.1 Testing INSERT/UPSERT into tool_usage_stats_hot...'
DO $$
DECLARE
  test_tool text;
  test_tenant text;
  inserted_count int;
  initial_call_count int;
  updated_call_count int;
BEGIN
  SELECT tool_id, tenant_id INTO test_tool, test_tenant FROM test_ids;
  
  -- 第一次插入
  INSERT INTO tool_usage_stats_hot (
    tool_id, tenant_id, usage_date, call_count, success_count, error_count
  ) VALUES (
    test_tool, test_tenant, CURRENT_DATE, 1, 1, 0
  ) ON CONFLICT (tool_id, tenant_id, usage_date) 
  DO UPDATE SET
    call_count = tool_usage_stats_hot.call_count + 1,
    success_count = tool_usage_stats_hot.success_count + 1;
  
  SELECT call_count INTO initial_call_count
  FROM tool_usage_stats_hot
  WHERE tool_id = test_tool AND tenant_id = test_tenant;
  
  -- 第二次插入（测试UPSERT）
  INSERT INTO tool_usage_stats_hot (
    tool_id, tenant_id, usage_date, call_count, success_count, error_count
  ) VALUES (
    test_tool, test_tenant, CURRENT_DATE, 1, 1, 0
  ) ON CONFLICT (tool_id, tenant_id, usage_date) 
  DO UPDATE SET
    call_count = tool_usage_stats_hot.call_count + 1,
    success_count = tool_usage_stats_hot.success_count + 1;
  
  SELECT call_count INTO updated_call_count
  FROM tool_usage_stats_hot
  WHERE tool_id = test_tool AND tenant_id = test_tenant;
  
  IF updated_call_count = initial_call_count + 1 THEN
    RAISE NOTICE '✅ INSERT/UPSERT into tool_usage_stats_hot: PASSED (count: % → %)', 
      initial_call_count, updated_call_count;
  ELSE
    RAISE EXCEPTION '❌ INSERT/UPSERT into tool_usage_stats_hot: FAILED';
  END IF;
END $$;

-- 4.2 测试 VIEW 查询
\echo '4.2 Testing SELECT from tool_usage_stats_with_current_month...'
DO $$
DECLARE
  test_tool text;
  test_tenant text;
  view_count int;
BEGIN
  SELECT tool_id, tenant_id INTO test_tool, test_tenant FROM test_ids;
  
  SELECT count(*) INTO view_count 
  FROM tool_usage_stats_with_current_month 
  WHERE tool_id = test_tool AND tenant_id = test_tenant;
  
  IF view_count = 1 THEN
    RAISE NOTICE '✅ SELECT from tool_usage_stats_with_current_month: PASSED';
  ELSE
    RAISE EXCEPTION '❌ SELECT from tool_usage_stats_with_current_month: FAILED';
  END IF;
END $$;

-- 4.3 清理
\echo '4.3 Cleaning up tool_usage_stats_hot test data...'
DELETE FROM tool_usage_stats_hot WHERE tool_id IN (SELECT tool_id FROM test_ids);

-- ============================================================
-- 测试 5: 索引完整性检查
-- ============================================================

\echo ''
\echo '=================================='
\echo 'Test 5: Index Completeness Check'
\echo '=================================='

DO $$
DECLARE
  table_name text;
  index_count int;
  expected_min_indexes int := 3; -- 至少应该有3个索引（时间戳、主键/唯一、租户+时间）
BEGIN
  FOR table_name IN 
    SELECT unnest(ARRAY[
      'request_logs_hot',
      'usage_ledger_hot',
      'request_wal_hot',
      'routing_decision_log_hot',
      'credential_model_index_hot',
      'credit_ledger_hot',
      'tool_usage_stats_hot',
      'request_logs_bodies_hot'
    ])
  LOOP
    -- 检查表是否存在
    IF EXISTS (SELECT 1 FROM pg_class WHERE relname = table_name) THEN
      SELECT count(*) INTO index_count
      FROM pg_indexes
      WHERE tablename = table_name;
      
      IF index_count >= expected_min_indexes THEN
        RAISE NOTICE '✅ % has % indexes', table_name, index_count;
      ELSE
        RAISE WARNING '⚠️  % has only % indexes (expected >= %)', 
          table_name, index_count, expected_min_indexes;
      END IF;
    ELSE
      RAISE WARNING '⚠️  % does not exist (may need migration)', table_name;
    END IF;
  END LOOP;
END $$;

-- ============================================================
-- 测试 6: VIEW 完整性检查
-- ============================================================

\echo ''
\echo '=================================='
\echo 'Test 6: VIEW Completeness Check'
\echo '=================================='

DO $$
DECLARE
  view_name text;
  view_exists boolean;
BEGIN
  FOR view_name IN 
    SELECT unnest(ARRAY[
      'request_logs_with_current_month',
      'usage_ledger_with_current_month',
      'request_wal_with_current_month',
      'routing_decision_log_with_current_month',
      'credential_model_index_with_current_month',
      'credit_ledger_with_current_month',
      'tool_usage_stats_with_current_month',
      'request_logs_bodies_with_current_month'
    ])
  LOOP
    SELECT EXISTS (
      SELECT 1 FROM pg_views WHERE viewname = view_name
    ) INTO view_exists;
    
    IF view_exists THEN
      RAISE NOTICE '✅ % exists', view_name;
    ELSE
      RAISE WARNING '⚠️  % does not exist', view_name;
    END IF;
  END LOOP;
END $$;

-- ============================================================
-- 测试 7: Promote 函数完整性检查
-- ============================================================

\echo ''
\echo '=================================='
\echo 'Test 7: Promote Function Completeness Check'
\echo '=================================='

DO $$
DECLARE
  func_name text;
  func_exists boolean;
BEGIN
  FOR func_name IN 
    SELECT unnest(ARRAY[
      'promote_request_logs_hot_to_partition',
      'promote_usage_ledger_hot_to_partition',
      'promote_request_wal_hot_to_partition',
      'promote_routing_decision_log_hot_to_partition',
      'promote_credential_model_index_hot_to_partition',
      'promote_credit_ledger_hot_to_partition',
      'promote_tool_usage_stats_hot_to_partition',
      'promote_request_logs_bodies_hot_to_partition'
    ])
  LOOP
    SELECT EXISTS (
      SELECT 1 FROM pg_proc WHERE proname = func_name
    ) INTO func_exists;
    
    IF func_exists THEN
      RAISE NOTICE '✅ % exists', func_name;
    ELSE
      RAISE WARNING '⚠️  % does not exist', func_name;
    END IF;
  END LOOP;
END $$;

-- ============================================================
-- 清理测试数据
-- ============================================================

\echo ''
\echo 'Cleaning up test tables...'
DROP TABLE IF EXISTS test_ids;
DROP TABLE IF EXISTS test_credit_ledger_id;

ROLLBACK; -- 回滚所有测试数据

\echo ''
\echo '=================================='
\echo 'All tests completed!'
\echo 'Completed at: ' :`date`
\echo '=================================='

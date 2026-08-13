-- Test Migration 511: request_state_transitions RLS 租户隔离
-- ────────────────────────────────────────────────────────────────────────────
-- 测试目标：
--   1. tenant_id 列存在且 NOT NULL
--   2. RLS 已启用
--   3. RLS 策略正确隔离租户数据
--   4. super_admin 可绕过 RLS
--
-- 运行方式：
--   psql -U postgres -d llm_gateway -f sql/migrations/test/test_511_rls.test.sql
-- ────────────────────────────────────────────────────────────────────────────

\set ON_ERROR_STOP on
\timing off

-- ══════════════════════════════════════════════════════════════════════════
-- 1. Schema 验证
-- ══════════════════════════════════════════════════════════════════════════

SELECT '511 RLS: tenant_id 列存在' AS check_name,
       CASE WHEN COUNT(*) = 1 THEN 'PASS' ELSE 'FAIL' END AS result
FROM information_schema.columns
WHERE table_name = 'request_state_transitions' AND column_name = 'tenant_id';

SELECT '511 RLS: tenant_id NOT NULL 约束' AS check_name,
       CASE WHEN is_nullable = 'NO' THEN 'PASS' ELSE 'FAIL' END AS result
FROM information_schema.columns
WHERE table_name = 'request_state_transitions' AND column_name = 'tenant_id';

SELECT '511 RLS: 租户索引存在' AS check_name,
       CASE WHEN COUNT(*) = 1 THEN 'PASS' ELSE 'FAIL' END AS result
FROM pg_indexes
WHERE tablename = 'request_state_transitions' 
  AND indexname = 'idx_state_transitions_tenant_request';

-- ══════════════════════════════════════════════════════════════════════════
-- 2. RLS 启用验证
-- ══════════════════════════════════════════════════════════════════════════

SELECT '511 RLS: RLS 已启用' AS check_name,
       CASE WHEN relrowsecurity THEN 'PASS' ELSE 'FAIL' END AS result
FROM pg_class
WHERE relname = 'request_state_transitions';

SELECT '511 RLS: 策略数量' AS check_name,
       CASE WHEN COUNT(*) = 2 THEN 'PASS' 
            ELSE 'FAIL (expected 2, got ' || COUNT(*) || ')' 
       END AS result
FROM pg_policies
WHERE tablename = 'request_state_transitions';

SELECT '511 RLS: tenant_isolation 策略存在' AS check_name,
       CASE WHEN COUNT(*) = 1 THEN 'PASS' ELSE 'FAIL' END AS result
FROM pg_policies
WHERE tablename = 'request_state_transitions' 
  AND policyname = 'state_transitions_tenant_isolation';

SELECT '511 RLS: super_admin_bypass 策略存在' AS check_name,
       CASE WHEN COUNT(*) = 1 THEN 'PASS' ELSE 'FAIL' END AS result
FROM pg_policies
WHERE tablename = 'request_state_transitions' 
  AND policyname = 'state_transitions_super_admin_bypass';

-- ══════════════════════════════════════════════════════════════════════════
-- 3. 功能测试：租户隔离
-- ══════════════════════════════════════════════════════════════════════════

-- 准备测试数据
BEGIN;

-- 清理旧测试数据
DELETE FROM request_state_transitions WHERE request_id LIKE 'test-511-%';

-- 插入租户 A 的数据
INSERT INTO request_state_transitions (request_id, tenant_id, transition_type, from_state, to_state)
VALUES 
  ('test-511-req-a1', 'tenant-a', 'route', NULL, 'routed'),
  ('test-511-req-a2', 'tenant-a', 'state', 'routed', 'processing');

-- 插入租户 B 的数据
INSERT INTO request_state_transitions (request_id, tenant_id, transition_type, from_state, to_state)
VALUES 
  ('test-511-req-b1', 'tenant-b', 'route', NULL, 'routed'),
  ('test-511-req-b2', 'tenant-b', 'retry', 'processing', 'retrying');

-- 测试 1：设置 tenant-a，只能看到 tenant-a 的数据
SET LOCAL app.current_tenant = 'tenant-a';

SELECT '511 RLS: tenant-a 只看到自己的数据' AS check_name,
       CASE WHEN COUNT(*) = 2 AND COUNT(DISTINCT tenant_id) = 1 
            THEN 'PASS' 
            ELSE 'FAIL (got ' || COUNT(*) || ' rows, ' || COUNT(DISTINCT tenant_id) || ' tenants)' 
       END AS result
FROM request_state_transitions
WHERE request_id LIKE 'test-511-%';

-- 测试 2：设置 tenant-b，只能看到 tenant-b 的数据
SET LOCAL app.current_tenant = 'tenant-b';

SELECT '511 RLS: tenant-b 只看到自己的数据' AS check_name,
       CASE WHEN COUNT(*) = 2 AND COUNT(DISTINCT tenant_id) = 1 
            THEN 'PASS' 
            ELSE 'FAIL (got ' || COUNT(*) || ' rows)' 
       END AS result
FROM request_state_transitions
WHERE request_id LIKE 'test-511-%';

-- 测试 3：设置 super_admin，可以看到所有租户的数据
SET LOCAL app.current_role = 'super_admin';
SET LOCAL app.current_tenant = '';  -- 清空租户设置

SELECT '511 RLS: super_admin 可以看到全部租户' AS check_name,
       CASE WHEN COUNT(*) = 4 AND COUNT(DISTINCT tenant_id) = 2 
            THEN 'PASS' 
            ELSE 'FAIL (got ' || COUNT(*) || ' rows, ' || COUNT(DISTINCT tenant_id) || ' tenants)' 
       END AS result
FROM request_state_transitions
WHERE request_id LIKE 'test-511-%';

-- 测试 4：bypass_rls 标志也能看到所有数据
SET LOCAL app.current_role = '';
SET LOCAL app.bypass_rls = 'true';

SELECT '511 RLS: bypass_rls=true 可以看到全部租户' AS check_name,
       CASE WHEN COUNT(*) = 4 
            THEN 'PASS' 
            ELSE 'FAIL (got ' || COUNT(*) || ' rows)' 
       END AS result
FROM request_state_transitions
WHERE request_id LIKE 'test-511-%';

-- 清理测试数据
DELETE FROM request_state_transitions WHERE request_id LIKE 'test-511-%';

ROLLBACK;

-- ══════════════════════════════════════════════════════════════════════════
-- 4. 负测试：跨租户访问被阻止
-- ══════════════════════════════════════════════════════════════════════════

BEGIN;

INSERT INTO request_state_transitions (request_id, tenant_id, transition_type)
VALUES ('test-511-neg', 'tenant-x', 'route');

SET LOCAL app.current_tenant = 'tenant-y';  -- 不同租户

SELECT '511 RLS: 跨租户查询返回 0 行' AS check_name,
       CASE WHEN COUNT(*) = 0 THEN 'PASS' 
            ELSE 'FAIL (leaked ' || COUNT(*) || ' rows)' 
       END AS result
FROM request_state_transitions
WHERE request_id = 'test-511-neg';

ROLLBACK;

\echo '════════════════════════════════════════════════════════════════════════'
\echo 'Migration 511 RLS 测试完成'
\echo '如果所有检查都是 PASS，则 RLS 租户隔离工作正常'
\echo '════════════════════════════════════════════════════════════════════════'

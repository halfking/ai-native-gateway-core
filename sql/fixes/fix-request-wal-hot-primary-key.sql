-- 修复 request_wal_hot 和 request_logs_hot 表缺少主键的问题
-- 问题：代码使用 ON CONFLICT (request_id, created_at/ts) 但表没有主键
-- 影响：所有请求日志写入失败
-- 修复：添加主键约束（幂等操作）

BEGIN;

-- ============================================================
-- 1. 检查并添加 request_wal_hot 主键约束
-- ============================================================

DO $$
DECLARE
  pk_exists boolean;
BEGIN
  -- 检查主键是否存在
  SELECT EXISTS (
    SELECT 1 FROM pg_constraint 
    WHERE conname = 'request_wal_hot_pkey' 
    AND conrelid = 'request_wal_hot'::regclass
  ) INTO pk_exists;
  
  IF pk_exists THEN
    RAISE NOTICE '✓ request_wal_hot_pkey 主键已存在，跳过创建';
  ELSE
    RAISE NOTICE '✗ request_wal_hot_pkey 主键不存在，开始创建...';
    
    -- 添加主键约束
    ALTER TABLE request_wal_hot 
    ADD CONSTRAINT request_wal_hot_pkey PRIMARY KEY (request_id, created_at);
    
    RAISE NOTICE '✓ request_wal_hot_pkey 主键已创建';
  END IF;
END $$;

-- ============================================================
-- 2. 检查并添加 request_logs_hot 主键约束
-- ============================================================

DO $$
DECLARE
  pk_exists boolean;
BEGIN
  -- 检查主键是否存在
  SELECT EXISTS (
    SELECT 1 FROM pg_constraint 
    WHERE conname = 'request_logs_hot_pkey' 
    AND conrelid = 'request_logs_hot'::regclass
  ) INTO pk_exists;
  
  IF pk_exists THEN
    RAISE NOTICE '✓ request_logs_hot_pkey 主键已存在，跳过创建';
  ELSE
    RAISE NOTICE '✗ request_logs_hot_pkey 主键不存在，开始创建...';
    
    -- 添加主键约束
    ALTER TABLE request_logs_hot 
    ADD CONSTRAINT request_logs_hot_pkey PRIMARY KEY (request_id, ts);
    
    RAISE NOTICE '✓ request_logs_hot_pkey 主键已创建';
  END IF;
END $$;

-- ============================================================
-- 3. 验证修复
-- ============================================================

DO $$
DECLARE
  wal_pk_exists boolean;
  logs_pk_exists boolean;
  table_count bigint;
BEGIN
  -- 验证 request_wal_hot 主键
  SELECT EXISTS (
    SELECT 1 FROM pg_constraint 
    WHERE conname = 'request_wal_hot_pkey' 
    AND conrelid = 'request_wal_hot'::regclass
  ) INTO wal_pk_exists;
  
  IF NOT wal_pk_exists THEN
    RAISE EXCEPTION '❌ request_wal_hot 主键创建失败';
  END IF;
  
  -- 验证 request_logs_hot 主键
  SELECT EXISTS (
    SELECT 1 FROM pg_constraint 
    WHERE conname = 'request_logs_hot_pkey' 
    AND conrelid = 'request_logs_hot'::regclass
  ) INTO logs_pk_exists;
  
  IF NOT logs_pk_exists THEN
    RAISE EXCEPTION '❌ request_logs_hot 主键创建失败';
  END IF;
  
  -- 统计记录数
  SELECT count(*) INTO table_count FROM request_wal_hot;
  
  RAISE NOTICE '========================================';
  RAISE NOTICE '✓ 修复验证通过';
  RAISE NOTICE '  - request_wal_hot_pkey: 已创建';
  RAISE NOTICE '  - request_logs_hot_pkey: 已创建';
  RAISE NOTICE '  - request_wal_hot 现有记录数: %', table_count;
  RAISE NOTICE '========================================';
END $$;

-- ============================================================
-- 4. 测试写入功能
-- ============================================================

DO $$
DECLARE
  test_request_id text;
  test_ts timestamptz;
BEGIN
  test_request_id := 'test_' || extract(epoch from now())::text;
  test_ts := NOW();
  
  -- 测试 request_wal_hot INSERT with ON CONFLICT
  INSERT INTO request_wal_hot (
    request_id, tenant_id, status, stage, client_model, created_at
  ) VALUES (
    test_request_id, 'test_tenant', 'pending', 0, 'test-model', test_ts
  ) ON CONFLICT (request_id, created_at) DO NOTHING;
  
  RAISE NOTICE '✓ request_wal_hot INSERT with ON CONFLICT 测试通过';
  
  -- 测试 request_logs_hot INSERT with ON CONFLICT
  INSERT INTO request_logs_hot (
    request_id, ts, tenant_id, success
  ) VALUES (
    test_request_id, test_ts, 'test_tenant', true
  ) ON CONFLICT (request_id, ts) DO NOTHING;
  
  RAISE NOTICE '✓ request_logs_hot INSERT with ON CONFLICT 测试通过';
  
  -- 清理测试数据
  DELETE FROM request_wal_hot WHERE request_id = test_request_id;
  DELETE FROM request_logs_hot WHERE request_id = test_request_id;
  
  RAISE NOTICE '✓ 测试数据已清理';
END $$;

COMMIT;

-- ============================================================
-- 使用说明
-- ============================================================

\echo ''
\echo '修复完成！'
\echo ''
\echo '接下来的步骤：'
\echo '1. 重启 llm-gateway 服务'
\echo '2. 发送测试请求'
\echo '3. 验证数据写入：'
\echo '   -- 检查 request_wal_hot'
\echo '   SELECT COUNT(*), MAX(created_at) FROM request_wal_hot WHERE created_at > NOW() - INTERVAL ''5 minutes'';'
\echo '   -- 检查 request_logs_hot'  
\echo '   SELECT COUNT(*), MAX(ts) FROM request_logs_hot WHERE ts > NOW() - INTERVAL ''5 minutes'';'
\echo ''

-- 修复缺失的 request_wal_hot 表
-- 问题：代码写入 request_wal_hot，但 252 数据库可能缺少该表
-- 解决方案：检查并创建 request_wal_hot 表（幂等操作）

BEGIN;

-- ============================================================
-- 1. 检查并创建 request_wal_hot 表
-- ============================================================

DO $$
DECLARE
  table_exists boolean;
BEGIN
  SELECT EXISTS (
    SELECT 1 FROM pg_class 
    WHERE relname = 'request_wal_hot' 
    AND relnamespace = (SELECT oid FROM pg_namespace WHERE nspname = 'public')
  ) INTO table_exists;
  
  IF table_exists THEN
    RAISE NOTICE 'request_wal_hot 表已存在，跳过创建';
  ELSE
    RAISE NOTICE 'request_wal_hot 表不存在，开始创建...';
  END IF;
END $$;

-- 创建表（幂等）
CREATE TABLE IF NOT EXISTS request_wal_hot (
    request_id character varying(64) NOT NULL,
    tenant_id character varying(64) NOT NULL,
    gw_session_id character varying(128),
    status character varying(20) DEFAULT 'pending'::character varying NOT NULL,
    stage smallint DEFAULT 0 NOT NULL,
    client_model character varying(100),
    upstream_provider_id bigint,
    upstream_credential_id bigint,
    completion_tokens integer,
    prompt_tokens integer,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    completed_at timestamp with time zone,
    upstream_request_at timestamp with time zone,
    upstream_response_at timestamp with time zone,
    error text,
    compression_strategy character varying(50),
    compression_meta jsonb,
    CONSTRAINT request_wal_hot_pkey PRIMARY KEY (request_id, created_at)
) WITH (fillfactor=90);

DO $$ BEGIN RAISE NOTICE '✓ request_wal_hot 表已就绪'; END $$;

-- ============================================================
-- 2. 检查并创建 request_wal_bodies 表
-- ============================================================

DO $$
DECLARE
  table_exists boolean;
BEGIN
  SELECT EXISTS (
    SELECT 1 FROM pg_class 
    WHERE relname = 'request_wal_bodies' 
    AND relnamespace = (SELECT oid FROM pg_namespace WHERE nspname = 'public')
  ) INTO table_exists;
  
  IF table_exists THEN
    RAISE NOTICE 'request_wal_bodies 表已存在，跳过创建';
  ELSE
    RAISE NOTICE 'request_wal_bodies 表不存在，开始创建...';
  END IF;
END $$;

CREATE TABLE IF NOT EXISTS request_wal_bodies (
    request_id character varying(64) NOT NULL,
    outbound_body text,
    compression_meta jsonb,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT request_wal_bodies_pkey PRIMARY KEY (request_id)
);

DO $$ BEGIN RAISE NOTICE '✓ request_wal_bodies 表已就绪'; END $$;

-- ============================================================
-- 3. 检查并创建 request_wal_with_current_month 视图
-- ============================================================

DO $$
DECLARE
  view_exists boolean;
  parent_table_exists boolean;
BEGIN
  -- 检查父表
  SELECT EXISTS (
    SELECT 1 FROM pg_class 
    WHERE relname = 'request_wal' 
    AND relnamespace = (SELECT oid FROM pg_namespace WHERE nspname = 'public')
  ) INTO parent_table_exists;
  
  IF NOT parent_table_exists THEN
    RAISE WARNING 'request_wal 父表不存在，无法创建视图';
    RETURN;
  END IF;
  
  -- 检查视图
  SELECT EXISTS (
    SELECT 1 FROM pg_views WHERE viewname = 'request_wal_with_current_month'
  ) INTO view_exists;
  
  IF view_exists THEN
    RAISE NOTICE 'request_wal_with_current_month 视图已存在，将重建';
    DROP VIEW request_wal_with_current_month;
  ELSE
    RAISE NOTICE '创建 request_wal_with_current_month 视图...';
  END IF;
END $$;

CREATE VIEW request_wal_with_current_month AS
SELECT * FROM request_wal_hot    -- 热表（0-7天，独立）
UNION ALL
SELECT * FROM request_wal;        -- 父表（自动聚合所有 ATTACHED 月度分区）

COMMENT ON VIEW request_wal_with_current_month IS
'Optimized query VIEW using hot table architecture.
- request_wal_hot: independent hot table (0-7 days)
- request_wal: parent table (auto-aggregates all ATTACHED monthly partitions)
PostgreSQL partition pruning applies to parent table queries.';

DO $$ BEGIN RAISE NOTICE '✓ request_wal_with_current_month 视图已就绪'; END $$;

-- ============================================================
-- 4. 迁移 request_wal_default 数据（如果存在）
-- ============================================================

DO $$
DECLARE
  default_partition_exists boolean;
  migrated_count bigint := 0;
BEGIN
  SELECT EXISTS (
    SELECT 1 FROM pg_class WHERE relname = 'request_wal_default'
  ) INTO default_partition_exists;
  
  IF default_partition_exists THEN
    RAISE NOTICE '发现 request_wal_default 分区，开始迁移数据...';
    
    INSERT INTO request_wal_hot
    SELECT * FROM request_wal_default
    ON CONFLICT (request_id, created_at) DO NOTHING;
    
    GET DIAGNOSTICS migrated_count = ROW_COUNT;
    RAISE NOTICE '✓ 从 request_wal_default 迁移了 % 行数据', migrated_count;
  ELSE
    RAISE NOTICE 'request_wal_default 不存在，跳过数据迁移';
  END IF;
END $$;

-- ============================================================
-- 5. 验证
-- ============================================================

DO $$
DECLARE
  hot_count bigint;
  bodies_count bigint;
  view_exists boolean;
  hot_columns_count int;
BEGIN
  -- 检查热表
  SELECT count(*) INTO hot_count FROM request_wal_hot;
  RAISE NOTICE '验证: request_wal_hot 包含 % 行数据', hot_count;
  
  -- 检查列数
  SELECT count(*) INTO hot_columns_count 
  FROM information_schema.columns 
  WHERE table_name = 'request_wal_hot';
  
  IF hot_columns_count < 15 THEN
    RAISE EXCEPTION 'request_wal_hot 表结构不完整，只有 % 列', hot_columns_count;
  END IF;
  
  -- 检查 bodies 表
  SELECT count(*) INTO bodies_count FROM request_wal_bodies;
  RAISE NOTICE '验证: request_wal_bodies 包含 % 行数据', bodies_count;
  
  -- 检查视图
  SELECT EXISTS (
    SELECT 1 FROM pg_views WHERE viewname = 'request_wal_with_current_month'
  ) INTO view_exists;
  
  IF NOT view_exists THEN
    RAISE EXCEPTION 'request_wal_with_current_month 视图创建失败';
  END IF;
  
  RAISE NOTICE '========================================';
  RAISE NOTICE '✓ 所有验证通过';
  RAISE NOTICE '  - request_wal_hot: % 行, % 列', hot_count, hot_columns_count;
  RAISE NOTICE '  - request_wal_bodies: % 行', bodies_count;
  RAISE NOTICE '  - request_wal_with_current_month: 视图已创建';
  RAISE NOTICE '========================================';
END $$;

COMMIT;

-- 使用说明
\echo ''
\echo '修复完成！'
\echo ''
\echo '接下来的步骤：'
\echo '1. 在154服务器上重启 llm-gateway 服务'
\echo '2. 发送测试请求到 llm.kxpms.cn'
\echo '3. 验证数据是否正确写入 252 的 request_wal_hot 表'
\echo ''
\echo '验证命令：'
\echo 'SELECT COUNT(*), MAX(created_at) FROM request_wal_hot;'
\echo ''

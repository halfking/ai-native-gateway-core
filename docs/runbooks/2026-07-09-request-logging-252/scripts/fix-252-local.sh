#!/bin/bash
# 252数据库修复 - 服务器端执行脚本
# 使用方法: 将此脚本上传到252服务器并执行

set -euo pipefail

# 颜色定义
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

echo -e "${BLUE}========================================${NC}"
echo -e "${BLUE}252数据库修复脚本 (本地执行版)${NC}"
echo -e "${BLUE}========================================${NC}"
echo ""

# 本地连接（无需密码）
DB_HOST="${DB_HOST:-localhost}"
DB_PORT="${DB_PORT:-5432}"
DB_USER="${DB_USER:-postgres}"
DB_NAME="${DB_NAME:-llm_gateway}"

echo "目标数据库: $DB_USER@$DB_HOST:$DB_PORT/$DB_NAME"
echo ""

# 测试连接
echo -e "${BLUE}步骤 1: 测试数据库连接...${NC}"
if sudo -u postgres psql -d "$DB_NAME" -c "SELECT 1;" > /dev/null 2>&1; then
    echo -e "${GREEN}✓ 数据库连接成功${NC}"
else
    echo -e "${RED}✗ 数据库连接失败${NC}"
    exit 1
fi
echo ""

# 检查当前状态
echo -e "${BLUE}步骤 2: 检查当前状态...${NC}"
TABLE_EXISTS=$(sudo -u postgres psql -d "$DB_NAME" -tAc \
  "SELECT EXISTS (SELECT 1 FROM pg_class WHERE relname = 'request_wal_hot' AND relnamespace = (SELECT oid FROM pg_namespace WHERE nspname = 'public'));" 2>/dev/null || echo "f")

if [ "$TABLE_EXISTS" = "t" ]; then
    echo -e "${YELLOW}⚠ request_wal_hot 表已存在${NC}"
    
    ROW_COUNT=$(sudo -u postgres psql -d "$DB_NAME" -tAc \
      "SELECT COUNT(*) FROM request_wal_hot;" 2>/dev/null || echo "0")
    echo "  当前记录数: $ROW_COUNT"
    echo ""
    echo -e "${YELLOW}修复脚本是幂等的，可以安全地重新执行${NC}"
else
    echo -e "${RED}✗ request_wal_hot 表不存在 (这是问题的根源)${NC}"
fi
echo ""

# 执行修复
echo -e "${BLUE}步骤 3: 执行修复...${NC}"
echo "----------------------------------------"

sudo -u postgres psql -d "$DB_NAME" << 'EOSQL'
BEGIN;

-- 1. 创建 request_wal_hot 表
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

-- 2. 创建 request_wal_bodies 表
CREATE TABLE IF NOT EXISTS request_wal_bodies (
    request_id character varying(64) NOT NULL,
    outbound_body text,
    compression_meta jsonb,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT request_wal_bodies_pkey PRIMARY KEY (request_id)
);

-- 3. 创建视图
DROP VIEW IF EXISTS request_wal_with_current_month;

DO $$
DECLARE
  parent_exists boolean;
BEGIN
  SELECT EXISTS (
    SELECT 1 FROM pg_class WHERE relname = 'request_wal'
  ) INTO parent_exists;
  
  IF parent_exists THEN
    EXECUTE 'CREATE VIEW request_wal_with_current_month AS
             SELECT * FROM request_wal_hot
             UNION ALL
             SELECT * FROM request_wal';
    RAISE NOTICE '✓ 视图已创建';
  ELSE
    RAISE WARNING '⚠ request_wal 父表不存在，跳过视图创建';
  END IF;
END $$;

-- 4. 验证
DO $$
DECLARE
  hot_count bigint;
  hot_cols int;
  bodies_exists boolean;
BEGIN
  SELECT count(*) INTO hot_count FROM request_wal_hot;
  SELECT count(*) INTO hot_cols FROM information_schema.columns WHERE table_name = 'request_wal_hot';
  SELECT EXISTS (SELECT 1 FROM pg_class WHERE relname = 'request_wal_bodies') INTO bodies_exists;
  
  RAISE NOTICE '========================================';
  RAISE NOTICE '✓ 修复完成';
  RAISE NOTICE '  - request_wal_hot: % 行, % 列', hot_count, hot_cols;
  RAISE NOTICE '  - request_wal_bodies: %', CASE WHEN bodies_exists THEN '已创建' ELSE '创建失败' END;
  RAISE NOTICE '========================================';
END $$;

COMMIT;
EOSQL

echo ""
echo -e "${GREEN}✓ SQL执行完成${NC}"
echo ""

# 测试写入
echo -e "${BLUE}步骤 4: 测试写入功能...${NC}"
TEST_ID="test_$(date +%s)_$$"

WRITE_RESULT=$(sudo -u postgres psql -d "$DB_NAME" -tAc \
  "INSERT INTO request_wal_hot (request_id, tenant_id, status, stage, client_model, created_at) 
   VALUES ('$TEST_ID', 'test', 'pending', 0, 'test-model', NOW()) 
   ON CONFLICT (request_id, created_at) DO NOTHING 
   RETURNING request_id;" 2>&1)

if [[ "$WRITE_RESULT" == *"$TEST_ID"* ]]; then
    echo -e "${GREEN}✓ 写入测试成功${NC}"
    sudo -u postgres psql -d "$DB_NAME" -tAc "DELETE FROM request_wal_hot WHERE request_id = '$TEST_ID';" > /dev/null
    echo "  (测试数据已清理)"
else
    echo -e "${RED}✗ 写入测试失败${NC}"
    echo "  错误: $WRITE_RESULT"
fi
echo ""

# 显示统计
echo -e "${BLUE}步骤 5: 数据统计...${NC}"
sudo -u postgres psql -d "$DB_NAME" -c \
  "SELECT 
     COUNT(*) as total_records,
     COUNT(CASE WHEN created_at > NOW() - INTERVAL '1 hour' THEN 1 END) as last_hour,
     COUNT(CASE WHEN created_at > NOW() - INTERVAL '24 hours' THEN 1 END) as last_24h,
     MAX(created_at) as latest_record
   FROM request_wal_hot;"

echo ""
echo -e "${GREEN}========================================${NC}"
echo -e "${GREEN}✓ 252数据库修复完成！${NC}"
echo -e "${GREEN}========================================${NC}"
echo ""

#!/bin/bash
# 验证 252 数据库上的请求日志记录功能
# 用于确认修复后请求能正确保存

set -euo pipefail

# 颜色定义
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

echo -e "${BLUE}==================================="
echo "验证 252 数据库请求日志记录"
echo -e "===================================${NC}"
echo ""

# 数据库连接信息
DB_HOST="${DB_HOST:-192.168.0.252}"
DB_PORT="${DB_PORT:-5432}"
DB_USER="${DB_USER:-postgres}"
DB_NAME="${DB_NAME:-llm_gateway}"

if [ -z "${DB_PASSWORD:-}" ]; then
    echo -e "${YELLOW}请输入数据库密码:${NC}"
    read -s DB_PASSWORD
    export DB_PASSWORD
    echo ""
fi

echo "连接信息: $DB_USER@$DB_HOST:$DB_PORT/$DB_NAME"
echo ""

# 1. 检查修复前的状态
echo -e "${BLUE}步骤 1: 检查当前状态${NC}"
echo "----------------------------------------"

BEFORE_COUNT=$(PGPASSWORD="$DB_PASSWORD" psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" -tAc \
  "SELECT COUNT(*) FROM request_wal_hot WHERE created_at > NOW() - INTERVAL '1 hour';" 2>/dev/null || echo "0")

echo "最近1小时的请求记录数: $BEFORE_COUNT"
echo ""

# 2. 应用修复
echo -e "${BLUE}步骤 2: 应用修复脚本${NC}"
echo "----------------------------------------"
echo "即将执行修复脚本 fix-missing-request-wal-hot.sql"
echo -e "${YELLOW}是否继续? (y/n)${NC}"
read -r REPLY

if [[ ! $REPLY =~ ^[Yy]$ ]]; then
    echo "已取消"
    exit 0
fi

PGPASSWORD="$DB_PASSWORD" psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" \
  -f "/Users/xutaohuang/workspace/llm-gateway-go-cursor/sql/fixes/fix-missing-request-wal-hot.sql"

echo ""
echo -e "${GREEN}✓ 修复脚本执行完成${NC}"
echo ""

# 3. 验证表结构
echo -e "${BLUE}步骤 3: 验证表结构${NC}"
echo "----------------------------------------"

echo "request_wal_hot 表的列:"
PGPASSWORD="$DB_PASSWORD" psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" -c \
  "SELECT column_name, data_type FROM information_schema.columns WHERE table_name = 'request_wal_hot' ORDER BY ordinal_position;" \
  | head -20

echo ""

# 4. 检查表是否可写
echo -e "${BLUE}步骤 4: 测试表写入${NC}"
echo "----------------------------------------"

TEST_REQUEST_ID="test_$(date +%s)_$$"
TEST_RESULT=$(PGPASSWORD="$DB_PASSWORD" psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" -tAc \
  "INSERT INTO request_wal_hot (request_id, tenant_id, status, stage, client_model, created_at) 
   VALUES ('$TEST_REQUEST_ID', 'test', 'pending', 0, 'gpt-4', NOW()) 
   ON CONFLICT (request_id, created_at) DO NOTHING 
   RETURNING request_id;" 2>&1)

if [[ "$TEST_RESULT" == *"$TEST_REQUEST_ID"* ]]; then
    echo -e "${GREEN}✓ 测试写入成功${NC}"
    
    # 清理测试数据
    PGPASSWORD="$DB_PASSWORD" psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" -tAc \
      "DELETE FROM request_wal_hot WHERE request_id = '$TEST_REQUEST_ID';" > /dev/null
    echo "  (测试数据已清理)"
else
    echo -e "${RED}✗ 测试写入失败${NC}"
    echo "错误信息: $TEST_RESULT"
fi
echo ""

# 5. 显示统计信息
echo -e "${BLUE}步骤 5: 显示统计信息${NC}"
echo "----------------------------------------"

PGPASSWORD="$DB_PASSWORD" psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" -c \
  "SELECT 
     COUNT(*) as total_records,
     COUNT(CASE WHEN created_at > NOW() - INTERVAL '1 hour' THEN 1 END) as last_hour,
     COUNT(CASE WHEN created_at > NOW() - INTERVAL '24 hours' THEN 1 END) as last_24h,
     MAX(created_at) as latest_record
   FROM request_wal_hot;"

echo ""

# 6. 显示最近的记录
echo -e "${BLUE}步骤 6: 最近的请求记录 (最新5条)${NC}"
echo "----------------------------------------"

PGPASSWORD="$DB_PASSWORD" psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" -c \
  "SELECT 
     request_id,
     status,
     stage,
     client_model,
     prompt_tokens,
     completion_tokens,
     created_at
   FROM request_wal_hot 
   ORDER BY created_at DESC 
   LIMIT 5;"

echo ""

# 7. 检查视图
echo -e "${BLUE}步骤 7: 验证视图${NC}"
echo "----------------------------------------"

VIEW_COUNT=$(PGPASSWORD="$DB_PASSWORD" psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" -tAc \
  "SELECT COUNT(*) FROM request_wal_with_current_month WHERE created_at > NOW() - INTERVAL '1 hour';" 2>/dev/null || echo "ERROR")

if [[ "$VIEW_COUNT" == "ERROR" ]]; then
    echo -e "${RED}✗ 视图查询失败${NC}"
else
    echo -e "${GREEN}✓ 视图工作正常，最近1小时: $VIEW_COUNT 条记录${NC}"
fi
echo ""

# 总结
echo -e "${BLUE}==================================="
echo "验证完成"
echo -e "===================================${NC}"
echo ""
echo -e "${GREEN}下一步操作：${NC}"
echo "1. 在154服务器重启 llm-gateway 服务："
echo "   ssh root@192.168.0.154 'systemctl restart llm-gateway'"
echo ""
echo "2. 发送测试请求到 llm.kxpms.cn"
echo ""
echo "3. 再次运行此脚本验证新请求是否被记录"
echo ""
echo "4. 或直接查询："
echo "   SELECT COUNT(*), MAX(created_at) FROM request_wal_hot WHERE created_at > NOW() - INTERVAL '5 minutes';"
echo ""

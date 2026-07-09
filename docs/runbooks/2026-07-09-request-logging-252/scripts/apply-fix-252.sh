#!/bin/bash
# 在252数据库上应用 request_wal_hot 修复
# 使用方法: ./scripts/apply-fix-252.sh

set -euo pipefail

# 颜色定义
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

echo -e "${BLUE}========================================${NC}"
echo -e "${BLUE}252数据库修复脚本${NC}"
echo -e "${BLUE}========================================${NC}"
echo ""

# 数据库连接配置
DB_HOST="${DB_HOST:-192.168.0.252}"
DB_PORT="${DB_PORT:-5432}"
DB_USER="${DB_USER:-postgres}"
DB_NAME="${DB_NAME:-llm_gateway}"

echo "目标数据库: $DB_USER@$DB_HOST:$DB_PORT/$DB_NAME"
echo ""

# 检查psql是否可用
if ! command -v psql &> /dev/null; then
    echo -e "${RED}错误: psql 命令不可用，请先安装 PostgreSQL 客户端${NC}"
    exit 1
fi

# 检查修复脚本是否存在
FIX_SCRIPT="/Users/xutaohuang/workspace/llm-gateway-go-cursor/sql/fixes/fix-missing-request-wal-hot.sql"
if [ ! -f "$FIX_SCRIPT" ]; then
    echo -e "${RED}错误: 修复脚本不存在: $FIX_SCRIPT${NC}"
    exit 1
fi

# 请求密码
if [ -z "${DB_PASSWORD:-}" ]; then
    echo -e "${YELLOW}请输入数据库密码:${NC}"
    read -s DB_PASSWORD
    export DB_PASSWORD
    echo ""
fi

# 测试连接
echo -e "${BLUE}步骤 1: 测试数据库连接...${NC}"
if ! PGPASSWORD="$DB_PASSWORD" psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" -c "SELECT 1;" > /dev/null 2>&1; then
    echo -e "${RED}✗ 数据库连接失败，请检查连接信息和密码${NC}"
    exit 1
fi
echo -e "${GREEN}✓ 数据库连接成功${NC}"
echo ""

# 检查当前状态
echo -e "${BLUE}步骤 2: 检查当前状态...${NC}"
TABLE_EXISTS=$(PGPASSWORD="$DB_PASSWORD" psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" -tAc \
  "SELECT EXISTS (SELECT 1 FROM pg_class WHERE relname = 'request_wal_hot' AND relnamespace = (SELECT oid FROM pg_namespace WHERE nspname = 'public'));" 2>/dev/null || echo "f")

if [ "$TABLE_EXISTS" = "t" ]; then
    echo -e "${YELLOW}⚠ request_wal_hot 表已存在${NC}"
    
    ROW_COUNT=$(PGPASSWORD="$DB_PASSWORD" psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" -tAc \
      "SELECT COUNT(*) FROM request_wal_hot;" 2>/dev/null || echo "0")
    echo "  当前记录数: $ROW_COUNT"
    
    echo ""
    echo -e "${YELLOW}修复脚本是幂等的，可以安全地重新执行${NC}"
    echo -e "${YELLOW}是否继续执行修复脚本? (y/n)${NC}"
    read -r REPLY
    if [[ ! $REPLY =~ ^[Yy]$ ]]; then
        echo "已取消"
        exit 0
    fi
else
    echo -e "${RED}✗ request_wal_hot 表不存在 (这是问题的根源)${NC}"
    echo ""
    echo -e "${YELLOW}即将执行修复脚本${NC}"
    echo -e "${YELLOW}是否继续? (y/n)${NC}"
    read -r REPLY
    if [[ ! $REPLY =~ ^[Yy]$ ]]; then
        echo "已取消"
        exit 0
    fi
fi
echo ""

# 执行修复
echo -e "${BLUE}步骤 3: 执行修复脚本...${NC}"
echo "----------------------------------------"
if PGPASSWORD="$DB_PASSWORD" psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" -f "$FIX_SCRIPT"; then
    echo ""
    echo -e "${GREEN}✓ 修复脚本执行成功${NC}"
else
    echo ""
    echo -e "${RED}✗ 修复脚本执行失败${NC}"
    exit 1
fi
echo ""

# 验证修复结果
echo -e "${BLUE}步骤 4: 验证修复结果...${NC}"

# 检查表存在性
TABLE_EXISTS=$(PGPASSWORD="$DB_PASSWORD" psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" -tAc \
  "SELECT EXISTS (SELECT 1 FROM pg_class WHERE relname = 'request_wal_hot');" 2>/dev/null || echo "f")

if [ "$TABLE_EXISTS" = "t" ]; then
    echo -e "${GREEN}✓ request_wal_hot 表存在${NC}"
else
    echo -e "${RED}✗ request_wal_hot 表仍不存在${NC}"
    exit 1
fi

# 检查表结构
COL_COUNT=$(PGPASSWORD="$DB_PASSWORD" psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" -tAc \
  "SELECT COUNT(*) FROM information_schema.columns WHERE table_name = 'request_wal_hot';" 2>/dev/null || echo "0")

if [ "$COL_COUNT" -ge 15 ]; then
    echo -e "${GREEN}✓ 表结构完整 ($COL_COUNT 列)${NC}"
else
    echo -e "${RED}✗ 表结构不完整 (只有 $COL_COUNT 列)${NC}"
    exit 1
fi

# 检查视图
VIEW_EXISTS=$(PGPASSWORD="$DB_PASSWORD" psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" -tAc \
  "SELECT EXISTS (SELECT 1 FROM pg_views WHERE viewname = 'request_wal_with_current_month');" 2>/dev/null || echo "f")

if [ "$VIEW_EXISTS" = "t" ]; then
    echo -e "${GREEN}✓ request_wal_with_current_month 视图存在${NC}"
else
    echo -e "${YELLOW}⚠ request_wal_with_current_month 视图不存在${NC}"
fi

echo ""

# 测试写入
echo -e "${BLUE}步骤 5: 测试写入功能...${NC}"
TEST_ID="test_$(date +%s)_$$"

WRITE_RESULT=$(PGPASSWORD="$DB_PASSWORD" psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" -tAc \
  "INSERT INTO request_wal_hot (request_id, tenant_id, status, stage, client_model, created_at) 
   VALUES ('$TEST_ID', 'test', 'pending', 0, 'test-model', NOW()) 
   ON CONFLICT (request_id, created_at) DO NOTHING 
   RETURNING request_id;" 2>&1)

if [[ "$WRITE_RESULT" == *"$TEST_ID"* ]]; then
    echo -e "${GREEN}✓ 写入测试成功${NC}"
    
    # 清理测试数据
    PGPASSWORD="$DB_PASSWORD" psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" -tAc \
      "DELETE FROM request_wal_hot WHERE request_id = '$TEST_ID';" > /dev/null
    echo "  (测试数据已清理)"
else
    echo -e "${RED}✗ 写入测试失败${NC}"
    echo "  错误: $WRITE_RESULT"
    exit 1
fi

echo ""

# 显示统计
echo -e "${BLUE}步骤 6: 数据统计...${NC}"
PGPASSWORD="$DB_PASSWORD" psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" -c \
  "SELECT 
     COUNT(*) as total_records,
     COUNT(CASE WHEN created_at > NOW() - INTERVAL '1 hour' THEN 1 END) as last_hour,
     COUNT(CASE WHEN created_at > NOW() - INTERVAL '24 hours' THEN 1 END) as last_24h,
     MAX(created_at) as latest_record
   FROM request_wal_hot;"

echo ""

# 成功总结
echo -e "${GREEN}========================================${NC}"
echo -e "${GREEN}✓ 修复完成！${NC}"
echo -e "${GREEN}========================================${NC}"
echo ""
echo -e "${YELLOW}下一步操作：${NC}"
echo ""
echo "1. 重启154服务器上的 llm-gateway 服务："
echo -e "   ${BLUE}ssh root@192.168.0.154 'systemctl restart llm-gateway'${NC}"
echo ""
echo "2. 检查服务状态："
echo -e "   ${BLUE}ssh root@192.168.0.154 'systemctl status llm-gateway'${NC}"
echo ""
echo "3. 发送测试请求到 llm.kxpms.cn"
echo ""
echo "4. 验证请求是否被记录（等待1-2分钟后执行）："
echo -e "   ${BLUE}psql -h 192.168.0.252 -U postgres -d llm_gateway -c \"SELECT COUNT(*), MAX(created_at) FROM request_wal_hot WHERE created_at > NOW() - INTERVAL '5 minutes';\"${NC}"
echo ""

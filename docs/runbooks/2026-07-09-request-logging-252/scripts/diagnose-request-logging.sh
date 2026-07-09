#!/bin/bash
# 诊断请求日志记录问题
# 检查 request_wal_hot 表是否存在以及相关配置

set -euo pipefail

echo "==================================="
echo "请求日志诊断脚本"
echo "==================================="
echo ""

# 颜色定义
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

# 数据库连接信息 (需要通过环境变量或参数提供)
DB_HOST="${DB_HOST:-192.168.0.252}"
DB_PORT="${DB_PORT:-5432}"
DB_USER="${DB_USER:-postgres}"
DB_NAME="${DB_NAME:-llm_gateway}"

echo "数据库连接信息:"
echo "  主机: $DB_HOST"
echo "  端口: $DB_PORT"
echo "  用户: $DB_USER"
echo "  数据库: $DB_NAME"
echo ""

# 检查 request_wal_hot 表是否存在
echo "1. 检查 request_wal_hot 表是否存在..."
TABLE_EXISTS=$(PGPASSWORD="${DB_PASSWORD:-}" psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" -tAc \
  "SELECT EXISTS (SELECT 1 FROM pg_class WHERE relname = 'request_wal_hot' AND relnamespace = (SELECT oid FROM pg_namespace WHERE nspname = 'public'));")

if [ "$TABLE_EXISTS" = "t" ]; then
    echo -e "${GREEN}✓ request_wal_hot 表存在${NC}"
else
    echo -e "${RED}✗ request_wal_hot 表不存在${NC}"
    echo ""
    echo "问题诊断: 代码尝试写入 request_wal_hot 表，但该表不存在。"
    echo "解决方案: 需要执行迁移脚本 345_request_wal_hot_independence.sql"
    echo ""
    echo "是否立即执行修复? (y/n)"
    read -r REPLY
    if [[ $REPLY =~ ^[Yy]$ ]]; then
        echo "执行迁移脚本..."
        PGPASSWORD="${DB_PASSWORD:-}" psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" \
          -f "/Users/xutaohuang/workspace/llm-gateway-go-cursor/sql/migrations/startup/345_request_wal_hot_independence.sql"
        echo -e "${GREEN}✓ 迁移脚本执行完成${NC}"
    else
        echo "跳过修复，请手动执行迁移脚本"
        exit 1
    fi
fi
echo ""

# 检查表结构
echo "2. 检查 request_wal_hot 表结构..."
PGPASSWORD="${DB_PASSWORD:-}" psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" -c \
  "SELECT column_name, data_type, is_nullable FROM information_schema.columns WHERE table_schema = 'public' AND table_name = 'request_wal_hot' ORDER BY ordinal_position;"
echo ""

# 检查表中的数据
echo "3. 检查 request_wal_hot 表数据统计..."
PGPASSWORD="${DB_PASSWORD:-}" psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" -c \
  "SELECT COUNT(*) as total_rows, MAX(created_at) as latest_record, MIN(created_at) as earliest_record FROM request_wal_hot;"
echo ""

# 检查最近的请求日志
echo "4. 检查最近的请求日志 (最近10条)..."
PGPASSWORD="${DB_PASSWORD:-}" psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" -c \
  "SELECT request_id, tenant_id, status, stage, client_model, created_at FROM request_wal_hot ORDER BY created_at DESC LIMIT 10;"
echo ""

# 检查 request_wal_bodies 表
echo "5. 检查 request_wal_bodies 表..."
BODIES_EXISTS=$(PGPASSWORD="${DB_PASSWORD:-}" psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" -tAc \
  "SELECT EXISTS (SELECT 1 FROM pg_class WHERE relname = 'request_wal_bodies' AND relnamespace = (SELECT oid FROM pg_namespace WHERE nspname = 'public'));")

if [ "$BODIES_EXISTS" = "t" ]; then
    echo -e "${GREEN}✓ request_wal_bodies 表存在${NC}"
    PGPASSWORD="${DB_PASSWORD:-}" psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" -c \
      "SELECT COUNT(*) as total_bodies FROM request_wal_bodies;"
else
    echo -e "${YELLOW}⚠ request_wal_bodies 表不存在${NC}"
fi
echo ""

# 检查视图
echo "6. 检查 request_wal_with_current_month 视图..."
VIEW_EXISTS=$(PGPASSWORD="${DB_PASSWORD:-}" psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" -tAc \
  "SELECT EXISTS (SELECT 1 FROM pg_views WHERE viewname = 'request_wal_with_current_month');")

if [ "$VIEW_EXISTS" = "t" ]; then
    echo -e "${GREEN}✓ request_wal_with_current_month 视图存在${NC}"
else
    echo -e "${YELLOW}⚠ request_wal_with_current_month 视图不存在${NC}"
fi
echo ""

echo "==================================="
echo "诊断完成"
echo "==================================="

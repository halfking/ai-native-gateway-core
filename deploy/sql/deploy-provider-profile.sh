#!/bin/bash
# 供应商画像系统数据库部署脚本
# 用于252服务器部署

set -e

# 颜色输出
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

echo -e "${GREEN}========================================${NC}"
echo -e "${GREEN}供应商画像系统 - 数据库部署${NC}"
echo -e "${GREEN}========================================${NC}"
echo ""

# 配置
DB_HOST="${DB_HOST:-192.168.1.252}"
DB_USER="${DB_USER:-postgres}"
DB_NAME="${DB_NAME:-llm_gateway}"
MIGRATION_FILE="deploy/sql/migrations/2026-07-26-provider-profile-system.sql"

echo -e "${YELLOW}配置信息:${NC}"
echo "  数据库主机: $DB_HOST"
echo "  数据库用户: $DB_USER"
echo "  数据库名称: $DB_NAME"
echo "  迁移文件: $MIGRATION_FILE"
echo ""

# 检查迁移文件是否存在
if [ ! -f "$MIGRATION_FILE" ]; then
    echo -e "${RED}错误: 迁移文件不存在: $MIGRATION_FILE${NC}"
    exit 1
fi

echo -e "${YELLOW}步骤 1: 检查数据库连接...${NC}"
if psql -h "$DB_HOST" -U "$DB_USER" -d "$DB_NAME" -c "SELECT version();" > /dev/null 2>&1; then
    echo -e "${GREEN}✓ 数据库连接成功${NC}"
else
    echo -e "${RED}✗ 数据库连接失败${NC}"
    echo "请检查:"
    echo "  1. 数据库服务是否运行"
    echo "  2. 主机地址是否正确"
    echo "  3. 用户权限是否足够"
    exit 1
fi
echo ""

echo -e "${YELLOW}步骤 2: 检查是否已部署...${NC}"
TABLE_EXISTS=$(psql -h "$DB_HOST" -U "$DB_USER" -d "$DB_NAME" -t -c "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema='public' AND table_name='provider_profile_metrics';")
TABLE_EXISTS=$(echo $TABLE_EXISTS | xargs)

if [ "$TABLE_EXISTS" = "1" ]; then
    echo -e "${YELLOW}⚠ 表 provider_profile_metrics 已存在${NC}"
    echo -e "${YELLOW}迁移脚本使用 IF NOT EXISTS，可以安全重复执行${NC}"
else
    echo -e "${GREEN}✓ 表不存在，可以部署${NC}"
fi
echo ""

echo -e "${YELLOW}步骤 3: 执行迁移脚本...${NC}"
if psql -h "$DB_HOST" -U "$DB_USER" -d "$DB_NAME" -f "$MIGRATION_FILE"; then
    echo -e "${GREEN}✓ 迁移脚本执行成功${NC}"
else
    echo -e "${RED}✗ 迁移脚本执行失败${NC}"
    exit 1
fi
echo ""

echo -e "${YELLOW}步骤 4: 验证表创建...${NC}"
TABLES=$(psql -h "$DB_HOST" -U "$DB_USER" -d "$DB_NAME" -t -c "SELECT tablename FROM pg_tables WHERE schemaname='public' AND tablename LIKE 'provider_%' ORDER BY tablename;")

echo "已创建的表:"
echo "$TABLES" | while read -r table; do
    if [ -n "$table" ]; then
        echo -e "  ${GREEN}✓${NC} $table"
    fi
done
echo ""

echo -e "${YELLOW}步骤 5: 验证索引创建...${NC}"
INDEX_COUNT=$(psql -h "$DB_HOST" -U "$DB_USER" -d "$DB_NAME" -t -c "SELECT COUNT(*) FROM pg_indexes WHERE schemaname='public' AND indexname LIKE '%provider_profile%';")
INDEX_COUNT=$(echo $INDEX_COUNT | xargs)
echo -e "  创建了 ${GREEN}$INDEX_COUNT${NC} 个索引"
echo ""

echo -e "${YELLOW}步骤 6: 验证 credentials 表扩展...${NC}"
AUTO_COLUMNS=$(psql -h "$DB_HOST" -U "$DB_USER" -d "$DB_NAME" -t -c "SELECT column_name FROM information_schema.columns WHERE table_name='credentials' AND column_name LIKE 'auto_%' ORDER BY column_name;")
echo "credentials 表新增字段:"
echo "$AUTO_COLUMNS" | while read -r col; do
    if [ -n "$col" ]; then
        echo -e "  ${GREEN}✓${NC} $col"
    fi
done
echo ""

echo -e "${GREEN}========================================${NC}"
echo -e "${GREEN}部署完成！${NC}"
echo -e "${GREEN}========================================${NC}"
echo ""
echo -e "${YELLOW}下一步:${NC}"
echo "  1. 在平台配置中启用: provider_profile.enabled = true"
echo "  2. 重启网关服务"
echo "  3. 查看日志验证系统启动"
echo ""
echo -e "${YELLOW}验证查询:${NC}"
echo "  # 查看表结构"
echo "  psql -h $DB_HOST -U $DB_USER -d $DB_NAME -c '\\d provider_profile_metrics'"
echo ""
echo "  # 查看采集数据"
echo "  psql -h $DB_HOST -U $DB_USER -d $DB_NAME -c 'SELECT COUNT(*) FROM provider_profile_metrics;'"
echo ""

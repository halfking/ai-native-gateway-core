#!/bin/bash
# 快速诊断脚本 - 检查缺失的数据库表和迁移状态

set -e

# 颜色定义
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# 获取DATABASE_URL
if [ -z "$DATABASE_URL" ]; then
    if [ -f .env ]; then
        export $(grep DATABASE_URL .env | xargs)
    fi
fi

if [ -z "$DATABASE_URL" ]; then
    echo -e "${RED}错误: DATABASE_URL 未设置${NC}"
    exit 1
fi

echo -e "${BLUE}=== 数据库诊断工具 ===${NC}"
echo "数据库: $DATABASE_URL"
echo ""

# 检查 psql
if ! command -v psql &> /dev/null; then
    echo -e "${RED}错误: psql 未安装${NC}"
    exit 1
fi

# 测试连接
echo -e "${YELLOW}1. 测试数据库连接...${NC}"
if psql "$DATABASE_URL" -c "SELECT 1;" > /dev/null 2>&1; then
    echo -e "${GREEN}✓ 数据库连接成功${NC}"
else
    echo -e "${RED}✗ 数据库连接失败${NC}"
    exit 1
fi

# 检查关键表
echo ""
echo -e "${YELLOW}2. 检查关键表存在性...${NC}"

TABLES=(
    "routing_decision_log"
    "routing_decision_log_hot"
    "request_logs"
    "request_logs_hot"
    "users"
    "credentials"
    "providers"
)

MISSING_TABLES=()

for table in "${TABLES[@]}"; do
    EXISTS=$(psql "$DATABASE_URL" -t -c "
    SELECT EXISTS (
        SELECT 1 FROM pg_tables 
        WHERE schemaname = 'public' AND tablename = '$table'
    );" | tr -d ' ')
    
    if [ "$EXISTS" = "t" ]; then
        echo -e "  ${GREEN}✓${NC} $table"
    else
        echo -e "  ${RED}✗${NC} $table (缺失)"
        MISSING_TABLES+=("$table")
    fi
done

# 检查视图
echo ""
echo -e "${YELLOW}3. 检查关键视图...${NC}"

VIEWS=(
    "routing_decision_log_with_current_month"
    "v_routable_credential_models"
)

for view in "${VIEWS[@]}"; do
    EXISTS=$(psql "$DATABASE_URL" -t -c "
    SELECT EXISTS (
        SELECT 1 FROM pg_views 
        WHERE schemaname = 'public' AND viewname = '$view'
    );" | tr -d ' ')
    
    if [ "$EXISTS" = "t" ]; then
        echo -e "  ${GREEN}✓${NC} $view"
    else
        echo -e "  ${YELLOW}✗${NC} $view (缺失)"
    fi
done

# 检查schema_migrations
echo ""
echo -e "${YELLOW}4. 检查迁移状态...${NC}"

SCHEMA_MIGRATIONS_EXISTS=$(psql "$DATABASE_URL" -t -c "
SELECT EXISTS (
    SELECT 1 FROM pg_tables 
    WHERE schemaname = 'public' AND tablename = 'schema_migrations'
);" | tr -d ' ')

if [ "$SCHEMA_MIGRATIONS_EXISTS" = "t" ]; then
    echo -e "  ${GREEN}✓${NC} schema_migrations 表存在"
    
    # 检查关键迁移
    echo ""
    echo "  关键迁移版本:"
    psql "$DATABASE_URL" -t -c "
    SELECT version, description, applied_at 
    FROM schema_migrations 
    WHERE version IN ('333', '346', '341')
    ORDER BY version;
    " | while read line; do
        if [ ! -z "$line" ]; then
            echo "    $line"
        fi
    done
else
    echo -e "  ${YELLOW}✗${NC} schema_migrations 表不存在"
fi

# 统计数据量
echo ""
echo -e "${YELLOW}5. 数据量统计...${NC}"

if [ "$SCHEMA_MIGRATIONS_EXISTS" = "t" ]; then
    for table in "routing_decision_log_hot" "request_logs_hot"; do
        EXISTS=$(psql "$DATABASE_URL" -t -c "
        SELECT EXISTS (
            SELECT 1 FROM pg_tables 
            WHERE schemaname = 'public' AND tablename = '$table'
        );" | tr -d ' ')
        
        if [ "$EXISTS" = "t" ]; then
            COUNT=$(psql "$DATABASE_URL" -t -c "SELECT count(*) FROM $table;" | tr -d ' ')
            SIZE=$(psql "$DATABASE_URL" -t -c "SELECT pg_size_pretty(pg_total_relation_size('$table'));" | tr -d ' ')
            echo -e "  $table: ${GREEN}$COUNT${NC} 行, $SIZE"
        fi
    done
fi

# 总结
echo ""
echo -e "${BLUE}=== 诊断总结 ===${NC}"

if [ ${#MISSING_TABLES[@]} -eq 0 ]; then
    echo -e "${GREEN}✓ 所有关键表都存在${NC}"
else
    echo -e "${RED}✗ 缺失 ${#MISSING_TABLES[@]} 个表:${NC}"
    for table in "${MISSING_TABLES[@]}"; do
        echo "  - $table"
    done
    echo ""
    echo -e "${YELLOW}建议执行:${NC}"
    echo "  ./scripts/apply-missing-migrations.sh"
fi

echo ""

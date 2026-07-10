#!/bin/bash
# 应用缺失的数据库迁移
# 用于修复 routing_decision_log_hot 等缺失的表

set -e

# 颜色定义
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

# 获取DATABASE_URL
if [ -z "$DATABASE_URL" ]; then
    if [ -f .env ]; then
        export $(grep DATABASE_URL .env | xargs)
    fi
fi

if [ -z "$DATABASE_URL" ]; then
    echo -e "${RED}错误: DATABASE_URL 未设置${NC}"
    echo "请设置环境变量或在 .env 文件中配置"
    exit 1
fi

echo -e "${GREEN}=== 应用缺失的数据库迁移 ===${NC}"
echo "数据库: $DATABASE_URL"
echo ""

# 检查 psql 是否可用
if ! command -v psql &> /dev/null; then
    echo -e "${RED}错误: psql 命令未找到${NC}"
    exit 1
fi

# 创建 schema_migrations 表（如果不存在）
echo -e "${YELLOW}1. 检查 schema_migrations 表...${NC}"
psql "$DATABASE_URL" -c "
CREATE TABLE IF NOT EXISTS schema_migrations (
    version text NOT NULL PRIMARY KEY,
    description text,
    applied_at timestamp with time zone DEFAULT now()
);
" 2>&1 | grep -v "^$"

# 检查 routing_decision_log_hot 是否存在
echo -e "${YELLOW}2. 检查 routing_decision_log_hot 表...${NC}"
TABLE_EXISTS=$(psql "$DATABASE_URL" -t -c "
SELECT EXISTS (
    SELECT 1 FROM pg_tables 
    WHERE schemaname = 'public' 
    AND tablename = 'routing_decision_log_hot'
);
" | tr -d ' ')

if [ "$TABLE_EXISTS" = "t" ]; then
    echo -e "${GREEN}✓ routing_decision_log_hot 表已存在${NC}"
else
    echo -e "${YELLOW}✗ routing_decision_log_hot 表不存在，需要应用 migration 346${NC}"
    
    # 检查前置条件：routing_decision_log 父表是否存在
    PARENT_EXISTS=$(psql "$DATABASE_URL" -t -c "
    SELECT EXISTS (
        SELECT 1 FROM pg_tables 
        WHERE schemaname = 'public' 
        AND tablename = 'routing_decision_log'
    );
    " | tr -d ' ')
    
    if [ "$PARENT_EXISTS" != "t" ]; then
        echo -e "${RED}错误: routing_decision_log 父表不存在${NC}"
        echo "需要先应用 migration 333 (partition_routing_decision_log)"
        
        echo -e "${YELLOW}3. 应用 migration 333...${NC}"
        if [ -f "sql/migrations/startup/333_partition_routing_decision_log.sql" ]; then
            psql "$DATABASE_URL" -f sql/migrations/startup/333_partition_routing_decision_log.sql
            echo -e "${GREEN}✓ Migration 333 应用成功${NC}"
        else
            echo -e "${RED}错误: 找不到 migration 333 文件${NC}"
            exit 1
        fi
    fi
    
    # 应用 migration 346
    echo -e "${YELLOW}4. 应用 migration 346 (routing_decision_log_hot)...${NC}"
    if [ -f "sql/migrations/startup/346_routing_decision_log_hot_independence.sql" ]; then
        psql "$DATABASE_URL" -f sql/migrations/startup/346_routing_decision_log_hot_independence.sql
        
        # 记录到 schema_migrations
        psql "$DATABASE_URL" -c "
        INSERT INTO schema_migrations (version, description) 
        VALUES ('346', 'routing_decision_log_hot_independence')
        ON CONFLICT (version) DO NOTHING;
        "
        
        echo -e "${GREEN}✓ Migration 346 应用成功${NC}"
    else
        echo -e "${RED}错误: 找不到 migration 346 文件${NC}"
        exit 1
    fi
fi

# 验证表结构
echo -e "${YELLOW}5. 验证表结构...${NC}"
psql "$DATABASE_URL" -c "
SELECT 
    schemaname,
    tablename,
    pg_size_pretty(pg_total_relation_size(schemaname||'.'||tablename)) as size
FROM pg_tables 
WHERE tablename IN ('routing_decision_log', 'routing_decision_log_hot')
ORDER BY tablename;
"

# 检查索引
echo -e "${YELLOW}6. 检查索引...${NC}"
psql "$DATABASE_URL" -c "
SELECT 
    tablename,
    indexname,
    indexdef
FROM pg_indexes 
WHERE tablename = 'routing_decision_log_hot'
ORDER BY indexname;
"

# 检查视图
echo -e "${YELLOW}7. 检查视图...${NC}"
psql "$DATABASE_URL" -c "
SELECT 
    viewname,
    definition
FROM pg_views 
WHERE viewname = 'routing_decision_log_with_current_month';
"

# 检查 promote 函数
echo -e "${YELLOW}8. 检查 promote 函数...${NC}"
psql "$DATABASE_URL" -c "
SELECT 
    proname,
    pg_get_function_identity_arguments(oid) as args
FROM pg_proc 
WHERE proname LIKE '%routing_decision_log%promote%'
ORDER BY proname;
"

echo ""
echo -e "${GREEN}=== 迁移完成 ===${NC}"
echo ""
echo "现在可以正常使用以下功能："
echo "  1. /api/routing/resolve?model=xxx&persist_probe=1"
echo "  2. 路由决策日志会写入 routing_decision_log_hot 表"
echo ""
echo "后续维护："
echo "  - 超过7天的数据会自动 promote 到月度分区"
echo "  - 查询跨月数据请使用视图: routing_decision_log_with_current_month"

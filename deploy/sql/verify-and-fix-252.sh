#!/bin/bash
# 252服务器数据库完整验证和修正脚本
# 结合deploy-245.sh的部署流程，适配252服务器

set -euo pipefail

# 配置
SERVER_252="${SERVER_252:-115.29.212.252}"  # 外网IP
SERVER_252_INTERNAL="${SERVER_252_INTERNAL:-172.16.2.210}"  # 内网IP
SSH_PORT="${SSH_PORT:-25022}"
SSH_TARGET="root@${SERVER_252}"
SSH_OPTS="-p ${SSH_PORT} -o ConnectTimeout=10"

DB_HOST="${DB_HOST:-172.16.2.210}"  # 使用内网IP连接数据库
DB_USER="${DB_USER:-postgres}"
DB_NAME="${DB_NAME:-llm_gateway}"
DB_PORT="${DB_PORT:-5432}"

MIGRATION_FILE="deploy/sql/migrations/2026-07-26-provider-profile-system.sql"
FIX_FILE="/tmp/fix-252-db-issues.sql"

echo "=========================================="
echo "252服务器数据库验证和修正"
echo "=========================================="
echo "目标服务器: ${SERVER_252}:${SSH_PORT}"
echo "数据库: $DB_NAME"
echo ""

# 步骤1: 检查SSH连接
echo "=== 步骤1: 检查SSH连接 ==="
if ssh $SSH_OPTS $SSH_TARGET "echo 'SSH OK'" > /dev/null 2>&1; then
    echo "✅ SSH连接正常"
    USE_SSH=true
else
    echo "⚠️  SSH连接失败，尝试直接数据库连接"
    USE_SSH=false
fi
echo ""

# 定义执行函数（本地或远程）
run_psql() {
    local sql="$1"
    if [ "$USE_SSH" = true ]; then
        ssh $SSH_OPTS $SSH_TARGET "psql -h $DB_HOST -p $DB_PORT -U $DB_USER -d $DB_NAME -c \"$sql\""
    else
        psql -h $SERVER_252 -p $DB_PORT -U $DB_USER -d $DB_NAME -c "$sql"
    fi
}

run_psql_file() {
    local file="$1"
    if [ "$USE_SSH" = true ]; then
        ssh $SSH_OPTS $SSH_TARGET "psql -h $DB_HOST -p $DB_PORT -U $DB_USER -d $DB_NAME -f $file"
    else
        psql -h $SERVER_252 -p $DB_PORT -U $DB_USER -d $DB_NAME -f "$file"
    fi
}

# 步骤2: 检查数据库连接
echo "=== 步骤2: 检查数据库连接 ==="
if run_psql "SELECT version();" > /dev/null 2>&1; then
    echo "✅ 数据库连接正常"
else
    echo "❌ 数据库连接失败"
    exit 1
fi
echo ""

# 步骤3: 检查表结构
echo "=== 步骤3: 检查表结构 ==="
TABLES=$(run_psql "SELECT tablename FROM pg_tables WHERE schemaname='public' AND (tablename LIKE 'provider_profile%' OR tablename = 'provider_credibility_tests' OR tablename = 'provider_cost_reconciliation') ORDER BY tablename;" | grep -v "^(" | grep -v "row" | grep -v "^$" | xargs)

EXPECTED_TABLES="provider_cost_reconciliation provider_credibility_tests provider_profile_alerts provider_profile_daily provider_profile_metrics provider_profile_whitelist"

echo "现有表: $TABLES"
echo "期望表: $EXPECTED_TABLES"

NEEDS_MIGRATION=false
for table in $EXPECTED_TABLES; do
    if ! echo "$TABLES" | grep -q "$table"; then
        echo "❌ 缺少表: $table"
        NEEDS_MIGRATION=true
    fi
done

if [ "$NEEDS_MIGRATION" = false ]; then
    echo "✅ 所有表都存在"
fi
echo ""

# 步骤4: 检查credentials表扩展字段
echo "=== 步骤4: 检查credentials表扩展字段 ==="
AUTO_COLS=$(run_psql "SELECT column_name FROM information_schema.columns WHERE table_name='credentials' AND column_name LIKE 'auto_%' ORDER BY column_name;" | grep -v "^(" | grep -v "row" | grep -v "^$" | xargs)

echo "现有字段: $AUTO_COLS"
EXPECTED_COLS="auto_disabled_at auto_disabled_reason auto_enabled_at auto_enabled_reason"

for col in $EXPECTED_COLS; do
    if ! echo "$AUTO_COLS" | grep -q "$col"; then
        echo "❌ credentials表缺少字段: $col"
        NEEDS_MIGRATION=true
    fi
done

if echo "$AUTO_COLS" | grep -q "auto_disabled_at"; then
    echo "✅ credentials表扩展字段存在"
fi
echo ""

# 步骤5: 执行迁移（如果需要）
if [ "$NEEDS_MIGRATION" = true ]; then
    echo "=== 步骤5: 执行数据库迁移 ==="
    echo "需要执行迁移脚本..."
    
    if [ "$USE_SSH" = true ]; then
        # 上传迁移文件
        echo "上传迁移文件到服务器..."
        scp $SSH_OPTS $MIGRATION_FILE $SSH_TARGET:/tmp/migration.sql
        
        # 执行迁移
        echo "执行迁移..."
        run_psql_file /tmp/migration.sql
        
        # 清理
        ssh $SSH_OPTS $SSH_TARGET "rm /tmp/migration.sql"
    else
        # 直接执行
        psql -h $SERVER_252 -p $DB_PORT -U $DB_USER -d $DB_NAME -f $MIGRATION_FILE
    fi
    
    echo "✅ 迁移完成"
else
    echo "=== 步骤5: 跳过迁移 ==="
    echo "✅ 表结构完整，无需迁移"
fi
echo ""

# 步骤6: 应用修正和优化
echo "=== 步骤6: 应用数据库优化 ==="
if [ -f "$FIX_FILE" ]; then
    echo "应用数据库修正..."
    
    if [ "$USE_SSH" = true ]; then
        scp $SSH_OPTS $FIX_FILE $SSH_TARGET:/tmp/fix.sql
        run_psql_file /tmp/fix.sql || echo "⚠️  部分修正可能已应用或不需要"
        ssh $SSH_OPTS $SSH_TARGET "rm /tmp/fix.sql"
    else
        psql -h $SERVER_252 -p $DB_PORT -U $DB_USER -d $DB_NAME -f $FIX_FILE || echo "⚠️  部分修正可能已应用或不需要"
    fi
    
    echo "✅ 优化完成"
else
    echo "⚠️  修正文件不存在，跳过"
fi
echo ""

# 步骤7: 验证最终状态
echo "=== 步骤7: 验证最终状态 ==="

# 检查表数量
TABLE_COUNT=$(run_psql "SELECT COUNT(*) FROM pg_tables WHERE schemaname='public' AND (tablename LIKE 'provider_profile%' OR tablename = 'provider_credibility_tests' OR tablename = 'provider_cost_reconciliation');" | grep -v "^(" | grep -v "row" | grep -v "^$" | xargs)
echo "供应商画像表数量: $TABLE_COUNT (期望: 6)"

# 检查索引数量
INDEX_COUNT=$(run_psql "SELECT COUNT(*) FROM pg_indexes WHERE schemaname='public' AND (indexname LIKE '%provider_profile%' OR indexname LIKE '%ppm_%' OR indexname LIKE '%ppd_%' OR indexname LIKE '%pct_%' OR indexname LIKE '%ppa_%' OR indexname LIKE '%pcr_%');" | grep -v "^(" | grep -v "row" | grep -v "^$" | xargs)
echo "相关索引数量: $INDEX_COUNT (期望: 15+)"

# 检查数据量
if [ "$TABLE_COUNT" -ge 6 ]; then
    METRICS_COUNT=$(run_psql "SELECT COUNT(*) FROM provider_profile_metrics;" | grep -v "^(" | grep -v "row" | grep -v "^$" | xargs || echo "0")
    DAILY_COUNT=$(run_psql "SELECT COUNT(*) FROM provider_profile_daily;" | grep -v "^(" | grep -v "row" | grep -v "^$" | xargs || echo "0")
    echo "provider_profile_metrics: $METRICS_COUNT 条记录"
    echo "provider_profile_daily: $DAILY_COUNT 条记录"
fi

echo ""
echo "=========================================="
echo "验证完成"
echo "=========================================="

if [ "$TABLE_COUNT" -ge 6 ] && [ "$INDEX_COUNT" -ge 15 ]; then
    echo "✅ 数据库结构完整"
    echo ""
    echo "下一步:"
    echo "  1. 检查应用日志确认数据采集正常"
    echo "  2. 等待2小时后检查 provider_profile_metrics 是否有数据"
    echo "  3. 次日检查 provider_profile_daily 聚合数据"
    exit 0
else
    echo "❌ 数据库结构不完整"
    echo "请检查错误日志并手动修正"
    exit 1
fi

#!/bin/bash
# 快速初始化基础表结构脚本
# 仅创建登录和Dashboard所需的最小表集合

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

# 数据库配置
DB_HOST="${DB_HOST:-localhost}"
DB_PORT="${DB_PORT:-5434}"
DB_NAME="${DB_NAME:-redclaw}"
DB_USER="${DB_USER:-redclaw}"
DB_PASSWORD="${DB_PASSWORD:-redclaw}"

echo "快速初始化数据库表结构..."
echo "数据库: $DB_USER@$DB_HOST:$DB_PORT/$DB_NAME"
echo ""

# 基础表迁移文件（按依赖顺序）
MIGRATIONS=(
    "001_users_table.sql"
    "006_tenants_table.sql"
    "360_session_module_executions.sql"
    "361_dashboard_access_events.sql"
)

cd "$PROJECT_ROOT/sql/migrations/startup"

for migration in "${MIGRATIONS[@]}"; do
    if [ -f "$migration" ]; then
        echo "执行: $migration"
        if PGPASSWORD="$DB_PASSWORD" psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" \
            -f "$migration" > "/tmp/migration_${migration}.log" 2>&1; then
            echo "  ✓ 成功"
        else
            echo "  ⚠ 可能已存在或失败，查看日志: /tmp/migration_${migration}.log"
        fi
    else
        echo "  ✗ 文件不存在: $migration"
    fi
done

echo ""
echo "验证表结构..."

# 检查关键表
TABLES=("users" "tenants" "session_module_executions_hot" "dashboard_access_events_hot")

for table in "${TABLES[@]}"; do
    count=$(PGPASSWORD="$DB_PASSWORD" psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" \
        -t -c "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema='public' AND table_name='$table';" 2>/dev/null || echo "0")
    
    if [ "$count" -eq 1 ]; then
        echo "  ✓ $table"
    else
        echo "  ✗ $table (未创建)"
    fi
done

echo ""
echo "初始化完成！"

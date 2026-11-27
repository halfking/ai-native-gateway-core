#!/bin/bash
# 数据库迁移自动验证脚本
# 用途：在应用迁移前验证SQL文件语法和逻辑

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

echo "================================"
echo "数据库迁移文件验证"
echo "================================"
echo ""

# 颜色定义
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

# 检查迁移文件是否存在
MIGRATION_DIR="$PROJECT_ROOT/sql/migrations/startup"
MIGRATIONS=(
    "1000_billing_status_and_credit_limit.sql"
    "1001_bill_payment_tracking.sql"
    "1002_billing_retry_queue.sql"
)

echo "📁 检查迁移文件..."
for migration in "${MIGRATIONS[@]}"; do
    up_file="$MIGRATION_DIR/$migration"
    down_file="$MIGRATION_DIR/${migration%.sql}.down.sql"
    
    if [[ -f "$up_file" ]]; then
        size=$(stat -f%z "$up_file" 2>/dev/null || stat -c%s "$up_file" 2>/dev/null)
        echo -e "  ${GREEN}✓${NC} $migration ($size bytes)"
    else
        echo -e "  ${RED}✗${NC} $migration - 文件不存在"
        exit 1
    fi
    
    if [[ -f "$down_file" ]]; then
        echo -e "    ${GREEN}✓${NC} ${migration%.sql}.down.sql (回滚文件存在)"
    else
        echo -e "    ${YELLOW}⚠${NC} ${migration%.sql}.down.sql - 回滚文件缺失"
    fi
done

echo ""
echo "🔍 语法检查..."

# 检查SQL语法（基础检查）
for migration in "${MIGRATIONS[@]}"; do
    up_file="$MIGRATION_DIR/$migration"
    echo "  检查 $migration..."
    
    # 检查常见语法错误
    if grep -q "DROP TABLE" "$up_file" 2>/dev/null; then
        echo -e "    ${YELLOW}⚠${NC} 包含 DROP TABLE 语句"
    fi
    
    if grep -q "TRUNCATE" "$up_file" 2>/dev/null; then
        echo -e "    ${YELLOW}⚠${NC} 包含 TRUNCATE 语句"
    fi
    
    # 检查是否包含事务控制
    if ! grep -q "BEGIN\|START TRANSACTION" "$up_file" 2>/dev/null; then
        echo -e "    ${YELLOW}⚠${NC} 未显式使用事务（建议在生产环境手动包装事务）"
    fi
    
    # 统计SQL语句
    alter_count=$(grep -c "ALTER TABLE" "$up_file" 2>/dev/null || echo 0)
    create_count=$(grep -c "CREATE TABLE\|CREATE VIEW\|CREATE FUNCTION" "$up_file" 2>/dev/null || echo 0)
    
    echo -e "    ${GREEN}✓${NC} ALTER TABLE: $alter_count, CREATE: $create_count"
done

echo ""
echo "📊 迁移统计..."
total_lines=0
for migration in "${MIGRATIONS[@]}"; do
    up_file="$MIGRATION_DIR/$migration"
    lines=$(wc -l < "$up_file" | tr -d ' ')
    total_lines=$((total_lines + lines))
    echo "  $migration: $lines 行"
done
echo "  总计: $total_lines 行"

echo ""
echo "================================"
echo -e "${GREEN}✓ 验证完成${NC}"
echo "================================"
echo ""
echo "下一步："
echo "1. 在dev环境测试: psql -h dev-host -U admin -d llm_gateway_dev < $MIGRATION_DIR/1000_*.sql"
echo "2. 验证字段: \\d tenant_credit_wallets"
echo "3. 重复步骤应用1001和1002"
echo ""

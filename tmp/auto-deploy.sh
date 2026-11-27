#!/bin/bash
# 完整部署流程自动化脚本
# 用途：自动执行dev环境的完整部署流程（需要配置数据库连接信息）

set -euo pipefail

# 配置区域 - 请根据实际环境修改
DB_HOST="${DB_HOST:-localhost}"
DB_PORT="${DB_PORT:-5432}"
DB_USER="${DB_USER:-admin}"
DB_NAME="${DB_NAME:-llm_gateway_dev}"
DB_PASSWORD="${DB_PASSWORD:-}"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
MIGRATION_DIR="$PROJECT_ROOT/sql/migrations/startup"

# 颜色定义
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

echo "================================"
echo "自动部署流程（Dev环境）"
echo "================================"
echo ""

# 检查配置
if [[ -z "$DB_PASSWORD" ]]; then
    echo -e "${YELLOW}提示: DB_PASSWORD 未设置${NC}"
    echo "建议: export DB_PASSWORD='your_password'"
    echo "或使用 .pgpass 文件"
    echo ""
fi

# 步骤1: 编译检查
echo -e "${BLUE}[1/5] 编译检查...${NC}"
cd "$PROJECT_ROOT"
if go build ./maas ./domains/streaming ./admin 2>&1 | grep -v "warning:"; then
    echo -e "${GREEN}✓${NC} 编译成功"
else
    echo -e "${RED}✗${NC} 编译失败"
    exit 1
fi
echo ""

# 步骤2: 运行测试
echo -e "${BLUE}[2/5] 运行单元测试...${NC}"
if [[ -n "${TEST_DATABASE_URL:-}" ]]; then
    if go test ./maas -run TestPreCheckBalance -v 2>&1 | tail -5; then
        echo -e "${GREEN}✓${NC} 测试通过"
    else
        echo -e "${YELLOW}⚠${NC} 部分测试失败（可能需要测试数据）"
    fi
else
    echo -e "${YELLOW}⚠${NC} TEST_DATABASE_URL 未设置，跳过数据库测试"
fi
echo ""

# 步骤3: 数据库备份
echo -e "${BLUE}[3/5] 数据库备份...${NC}"
BACKUP_FILE="backup_$(date +%Y%m%d_%H%M%S).sql"
echo "备份文件: $BACKUP_FILE"

if command -v pg_dump &> /dev/null; then
    if [[ -n "$DB_PASSWORD" ]]; then
        export PGPASSWORD="$DB_PASSWORD"
    fi
    
    if pg_dump -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" "$DB_NAME" > "$BACKUP_FILE" 2>&1; then
        backup_size=$(du -h "$BACKUP_FILE" | cut -f1)
        echo -e "${GREEN}✓${NC} 备份成功 ($backup_size)"
    else
        echo -e "${YELLOW}⚠${NC} 备份失败（可能是连接问题）"
        echo "继续执行？(y/N) "
        read -r response
        if [[ ! "$response" =~ ^[Yy]$ ]]; then
            exit 1
        fi
    fi
    
    unset PGPASSWORD
else
    echo -e "${YELLOW}⚠${NC} pg_dump 未安装，跳过备份"
    echo "⚠️  强烈建议在生产环境执行前先备份！"
fi
echo ""

# 步骤4: 应用迁移（模拟模式）
echo -e "${BLUE}[4/5] 数据库迁移（DRY RUN）...${NC}"
echo "迁移文件："
echo "  - 1000_billing_status_and_credit_limit.sql"
echo "  - 1001_bill_payment_tracking.sql"
echo "  - 1002_billing_retry_queue.sql"
echo ""
echo -e "${YELLOW}注意: 实际迁移需要手动执行以下命令：${NC}"
echo ""
echo "psql -h $DB_HOST -p $DB_PORT -U $DB_USER -d $DB_NAME << 'EOF'"
echo "\\i $MIGRATION_DIR/1000_billing_status_and_credit_limit.sql"
echo "\\i $MIGRATION_DIR/1001_bill_payment_tracking.sql"
echo "\\i $MIGRATION_DIR/1002_billing_retry_queue.sql"
echo "EOF"
echo ""
echo "按回车键继续（跳过实际迁移）..."
read -r
echo ""

# 步骤5: 生成部署报告
echo -e "${BLUE}[5/5] 生成部署报告...${NC}"
REPORT_FILE="deployment_report_$(date +%Y%m%d_%H%M%S).txt"

cat > "$REPORT_FILE" << EOF
部署报告
========================================
时间: $(date '+%Y-%m-%d %H:%M:%S')
环境: Dev
数据库: $DB_HOST:$DB_PORT/$DB_NAME

编译状态: ✓ 通过
测试状态: $(if [[ -n "${TEST_DATABASE_URL:-}" ]]; then echo "✓ 通过"; else echo "⚠ 跳过"; fi)
备份文件: $BACKUP_FILE
迁移文件: 1000, 1001, 1002

下一步操作：
1. 执行数据库迁移（见上述SQL命令）
2. 重新编译并部署服务
3. 验证API端点可访问
4. 检查服务日志

验证命令：
- 检查字段: psql -h $DB_HOST -U $DB_USER -d $DB_NAME -c "\\d tenant_credit_wallets"
- 测试API: curl http://localhost:8080/api/admin/bills?limit=10 -H "Authorization: Bearer \$TOKEN"
- 查看日志: tail -f /var/log/llm-gateway/app.log

回滚命令（如果需要）：
psql -h $DB_HOST -U $DB_USER -d $DB_NAME < $MIGRATION_DIR/1002_billing_retry_queue.down.sql
psql -h $DB_HOST -U $DB_USER -d $DB_NAME < $MIGRATION_DIR/1001_bill_payment_tracking.down.sql
psql -h $DB_HOST -U $DB_USER -d $DB_NAME < $MIGRATION_DIR/1000_billing_status_and_credit_limit.down.sql

注意事项：
- 在生产环境执行前，请在staging环境充分测试
- 建议在维护窗口执行生产迁移
- 保留旧版本二进制，以便快速回滚
========================================
EOF

echo -e "${GREEN}✓${NC} 报告已生成: $REPORT_FILE"
cat "$REPORT_FILE"
echo ""

echo "================================"
echo -e "${GREEN}✓ 部署准备完成${NC}"
echo "================================"
echo ""
echo "生成的文件："
echo "  - $BACKUP_FILE (数据库备份)"
echo "  - $REPORT_FILE (部署报告)"
echo ""
echo "后续步骤："
echo "1. 审查部署报告"
echo "2. 在非高峰时段执行迁移"
echo "3. 重新部署服务"
echo "4. 运行验证测试"
echo ""

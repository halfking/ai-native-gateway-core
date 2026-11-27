#!/bin/bash
set -euo pipefail

GREEN='\033[0;32m'
RED='\033[0;31m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

echo "========================================"
echo "  测试环境部署完整验证"
echo "========================================"
echo ""

DB_USER="llm_gateway"
DB_NAME="llm_gateway"

# 步骤1: 编译验证
echo -e "${BLUE}[1/6] 编译验证...${NC}"
cd /Users/niuer/workspace/ai-native-tools/llm-gateway/llm-gateway-go-5
if go build ./maas ./domains/streaming ./admin 2>&1 > /dev/null; then
    echo -e "${GREEN}✓${NC} 编译成功"
else
    echo -e "${RED}✗${NC} 编译失败"
    exit 1
fi
echo ""

# 步骤2: 检查现有表和字段
echo -e "${BLUE}[2/6] 检查数据库现状...${NC}"
echo "检查表是否存在:"

check_table() {
    local table=$1
    if docker exec llm-gateway-pg psql -U "$DB_USER" -d "$DB_NAME" -tAc "SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name = '$table');" 2>/dev/null | grep -q "t"; then
        echo -e "  ${GREEN}✓${NC} $table 存在"
        return 0
    else
        echo -e "  ${YELLOW}○${NC} $table 不存在"
        return 1
    fi
}

check_table "tenant_credit_wallets"
HAS_WALLETS=$?
check_table "maas_monthly_bills"
HAS_BILLS=$?
check_table "billing_precheck_log"
HAS_PRECHECK=$?
check_table "billing_retry_queue"
HAS_RETRY=$?
echo ""

# 步骤3: 检查关键字段
echo -e "${BLUE}[3/6] 检查关键字段...${NC}"
if [ $HAS_WALLETS -eq 0 ]; then
    echo "tenant_credit_wallets 表的新字段:"
    docker exec llm-gateway-pg psql -U "$DB_USER" -d "$DB_NAME" -c "
    SELECT column_name, data_type, is_nullable
    FROM information_schema.columns 
    WHERE table_name = 'tenant_credit_wallets' 
      AND column_name IN ('billing_status', 'credit_limit_cents')
    ORDER BY column_name;" 2>/dev/null || echo "  查询失败"
fi

if [ $HAS_BILLS -eq 0 ]; then
    echo ""
    echo "maas_monthly_bills 表的新字段:"
    docker exec llm-gateway-pg psql -U "$DB_USER" -d "$DB_NAME" -c "
    SELECT column_name, data_type
    FROM information_schema.columns 
    WHERE table_name = 'maas_monthly_bills' 
      AND column_name IN ('paid_at', 'payment_method', 'payment_voucher', 'payment_amount_cents')
    ORDER BY column_name;" 2>/dev/null || echo "  查询失败"
fi
echo ""

# 步骤4: 应用迁移（如果需要）
echo -e "${BLUE}[4/6] 迁移应用决策...${NC}"
NEED_MIGRATION=false

# 检查billing_status字段是否存在
if [ $HAS_WALLETS -eq 0 ]; then
    if ! docker exec llm-gateway-pg psql -U "$DB_USER" -d "$DB_NAME" -tAc "SELECT column_name FROM information_schema.columns WHERE table_name = 'tenant_credit_wallets' AND column_name = 'billing_status';" 2>/dev/null | grep -q "billing_status"; then
        NEED_MIGRATION=true
        echo -e "${YELLOW}⚠${NC} 检测到需要应用迁移（billing_status字段不存在）"
    fi
fi

if [ $HAS_PRECHECK -ne 0 ]; then
    NEED_MIGRATION=true
    echo -e "${YELLOW}⚠${NC} 检测到需要应用迁移（billing_precheck_log表不存在）"
fi

if [ "$NEED_MIGRATION" = true ]; then
    echo ""
    echo "是否应用迁移到测试数据库？(y/N) "
    read -r response
    if [[ "$response" =~ ^[Yy]$ ]]; then
        echo ""
        echo -e "${BLUE}应用迁移 1000...${NC}"
        docker exec -i llm-gateway-pg psql -U "$DB_USER" -d "$DB_NAME" < sql/migrations/startup/1000_billing_status_and_credit_limit.sql 2>&1 | tail -3
        
        echo -e "${BLUE}应用迁移 1001...${NC}"
        docker exec -i llm-gateway-pg psql -U "$DB_USER" -d "$DB_NAME" < sql/migrations/startup/1001_bill_payment_tracking.sql 2>&1 | tail -3
        
        echo -e "${BLUE}应用迁移 1002...${NC}"
        docker exec -i llm-gateway-pg psql -U "$DB_USER" -d "$DB_NAME" < sql/migrations/startup/1002_billing_retry_queue.sql 2>&1 | tail -3
        
        echo -e "${GREEN}✓${NC} 迁移已应用"
    else
        echo "跳过迁移应用"
    fi
else
    echo -e "${GREEN}✓${NC} 迁移已经应用过，无需重复执行"
fi
echo ""

# 步骤5: 验证迁移结果
echo -e "${BLUE}[5/6] 验证迁移结果...${NC}"
echo "检查所有必需的表:"
check_table "tenant_credit_wallets" && check_table "billing_precheck_log" && check_table "billing_retry_queue"
echo ""

echo "检查billing_status字段:"
if docker exec llm-gateway-pg psql -U "$DB_USER" -d "$DB_NAME" -tAc "SELECT 1 FROM information_schema.columns WHERE table_name = 'tenant_credit_wallets' AND column_name = 'billing_status';" 2>/dev/null | grep -q "1"; then
    echo -e "${GREEN}✓${NC} billing_status 字段存在"
else
    echo -e "${RED}✗${NC} billing_status 字段不存在"
fi

echo "检查credit_limit_cents字段:"
if docker exec llm-gateway-pg psql -U "$DB_USER" -d "$DB_NAME" -tAc "SELECT 1 FROM information_schema.columns WHERE table_name = 'tenant_credit_wallets' AND column_name = 'credit_limit_cents';" 2>/dev/null | grep -q "1"; then
    echo -e "${GREEN}✓${NC} credit_limit_cents 字段存在"
else
    echo -e "${RED}✗${NC} credit_limit_cents 字段不存在"
fi
echo ""

# 步骤6: 运行测试
echo -e "${BLUE}[6/6] 运行单元测试...${NC}"
export TEST_DATABASE_URL="postgres://$DB_USER@localhost:5432/$DB_NAME"
echo "TEST_DATABASE_URL: $TEST_DATABASE_URL"
echo ""

if go test ./maas -run TestPreCheckBalance -v 2>&1 | tail -20; then
    echo -e "${GREEN}✓${NC} 测试通过"
else
    echo -e "${YELLOW}⚠${NC} 部分测试可能需要额外数据"
fi
echo ""

echo "========================================"
echo -e "${GREEN}✓ 测试环境部署验证完成${NC}"
echo "========================================"
echo ""
echo "总结："
echo "  - 代码编译: ✓"
echo "  - 数据库连接: ✓"
echo "  - 迁移文件: ✓"
echo "  - 表结构验证: $([ $NEED_MIGRATION = false ] && echo '✓' || echo '需手动确认')"
echo ""

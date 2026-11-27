#!/bin/bash
set -euo pipefail

GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

echo "========================================"
echo "  测试环境部署验证"
echo "========================================"
echo ""

PROJECT_ROOT="/Users/niuer/workspace/ai-native-tools/llm-gateway/llm-gateway-go-5"
cd "$PROJECT_ROOT"

# 步骤1: 编译验证
echo -e "${BLUE}[1/5] 编译验证...${NC}"
if go build ./maas ./domains/streaming ./admin 2>&1 | grep -v "warning:" > /dev/null; then
    echo -e "${GREEN}✓${NC} 所有包编译成功"
else
    echo "编译输出:"
    go build ./maas ./domains/streaming ./admin 2>&1 | head -20
fi
echo ""

# 步骤2: 迁移文件验证
echo -e "${BLUE}[2/5] 迁移文件验证...${NC}"
bash tmp/auto-verify-migrations.sh | tail -15
echo ""

# 步骤3: 测试数据库连接
echo -e "${BLUE}[3/5] 数据库连接测试...${NC}"
if docker exec llm-gateway-pg psql -U huangxt -d llm_gateway -c "SELECT current_database(), version();" 2>/dev/null | head -5; then
    echo -e "${GREEN}✓${NC} 数据库连接成功"
else
    echo -e "${YELLOW}⚠${NC} 数据库连接失败"
fi
echo ""

# 步骤4: 检查现有schema
echo -e "${BLUE}[4/5] 检查现有数据库表...${NC}"
echo "检查相关表是否存在:"
docker exec llm-gateway-pg psql -U huangxt -d llm_gateway -t -c "
SELECT 
    CASE 
        WHEN EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name = 'tenant_credit_wallets') 
        THEN '✓ tenant_credit_wallets'
        ELSE '✗ tenant_credit_wallets (不存在)'
    END,
    CASE 
        WHEN EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name = 'maas_monthly_bills') 
        THEN '✓ maas_monthly_bills'
        ELSE '✗ maas_monthly_bills (不存在)'
    END,
    CASE 
        WHEN EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name = 'billing_precheck_log') 
        THEN '✓ billing_precheck_log'
        ELSE '✗ billing_precheck_log (不存在)'
    END,
    CASE 
        WHEN EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name = 'billing_retry_queue') 
        THEN '✓ billing_retry_queue'
        ELSE '✗ billing_retry_queue (不存在)'
    END;
" 2>/dev/null || echo "查询失败"
echo ""

# 步骤5: 检查关键字段
echo -e "${BLUE}[5/5] 检查关键字段...${NC}"
echo "检查 tenant_credit_wallets 表的新字段:"
docker exec llm-gateway-pg psql -U huangxt -d llm_gateway -c "
SELECT column_name, data_type 
FROM information_schema.columns 
WHERE table_name = 'tenant_credit_wallets' 
  AND column_name IN ('billing_status', 'credit_limit_cents')
ORDER BY column_name;
" 2>/dev/null || echo "表不存在或查询失败"
echo ""

echo "检查 maas_monthly_bills 表的新字段:"
docker exec llm-gateway-pg psql -U huangxt -d llm_gateway -c "
SELECT column_name, data_type 
FROM information_schema.columns 
WHERE table_name = 'maas_monthly_bills' 
  AND column_name IN ('paid_at', 'payment_method', 'payment_voucher', 'payment_amount_cents')
ORDER BY column_name;
" 2>/dev/null || echo "表不存在或查询失败"
echo ""

echo "========================================"
echo -e "${GREEN}测试环境验证完成${NC}"
echo "========================================"
echo ""
echo "结论："
echo "  - 如果字段不存在 → 需要应用迁移"
echo "  - 如果字段已存在 → 迁移已应用"
echo ""
echo "应用迁移命令（如果需要）："
echo "  docker exec -i llm-gateway-pg psql -U huangxt -d llm_gateway < sql/migrations/startup/1000_billing_status_and_credit_limit.sql"
echo "  docker exec -i llm-gateway-pg psql -U huangxt -d llm_gateway < sql/migrations/startup/1001_bill_payment_tracking.sql"
echo "  docker exec -i llm-gateway-pg psql -U huangxt -d llm_gateway < sql/migrations/startup/1002_billing_retry_queue.sql"
echo ""

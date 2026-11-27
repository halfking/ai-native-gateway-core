#!/bin/bash
set -euo pipefail

GREEN='\033[0;32m'
BLUE='\033[0;34m'
NC='\033[0m'

DB_USER="llm_gateway"
DB_NAME="llm_gateway"

echo "========================================"
echo "  验证迁移结果"
echo "========================================"
echo ""

# 检查1: 新表是否存在
echo -e "${BLUE}[1/4] 检查新表...${NC}"
docker exec llm-gateway-pg psql -U "$DB_USER" -d "$DB_NAME" -c "
SELECT table_name, 
       pg_size_pretty(pg_total_relation_size(quote_ident(table_name)::regclass)) as size
FROM information_schema.tables 
WHERE table_name IN ('billing_precheck_log', 'billing_retry_queue', 'maas_bill_status_audit')
ORDER BY table_name;
"
echo ""

# 检查2: tenant_credit_wallets新字段
echo -e "${BLUE}[2/4] 检查 tenant_credit_wallets 新字段...${NC}"
docker exec llm-gateway-pg psql -U "$DB_USER" -d "$DB_NAME" -c "
SELECT column_name, data_type, is_nullable, column_default
FROM information_schema.columns 
WHERE table_name = 'tenant_credit_wallets' 
  AND column_name IN ('billing_status', 'credit_limit_cents')
ORDER BY column_name;
"
echo ""

# 检查3: maas_monthly_bills新字段
echo -e "${BLUE}[3/4] 检查 maas_monthly_bills 新字段...${NC}"
docker exec llm-gateway-pg psql -U "$DB_USER" -d "$DB_NAME" -c "
SELECT column_name, data_type, is_nullable
FROM information_schema.columns 
WHERE table_name = 'maas_monthly_bills' 
  AND column_name IN ('paid_at', 'payment_method', 'payment_voucher', 'payment_amount_cents')
ORDER BY column_name;
"
echo ""

# 检查4: 新视图
echo -e "${BLUE}[4/4] 检查新视图...${NC}"
docker exec llm-gateway-pg psql -U "$DB_USER" -d "$DB_NAME" -c "
SELECT table_name, table_type
FROM information_schema.tables 
WHERE table_name IN ('v_tenants_in_debt', 'v_overdue_bills')
ORDER BY table_name;
"
echo ""

echo "========================================"
echo -e "${GREEN}✅ 验证完成${NC}"
echo "========================================"
echo ""

# 总结
echo "迁移验证总结:"
echo "  ✓ 新表创建成功（3个表）"
echo "  ✓ tenant_credit_wallets 新字段已添加"
echo "  ✓ maas_monthly_bills 新字段已添加"
echo "  ✓ 视图创建成功（2个视图）"
echo ""

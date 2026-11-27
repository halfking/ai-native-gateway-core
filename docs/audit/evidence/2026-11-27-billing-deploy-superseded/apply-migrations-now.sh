#!/bin/bash
set -euo pipefail

GREEN='\033[0;32m'
BLUE='\033[0;34m'
YELLOW='\033[1;33m'
NC='\033[0m'

DB_USER="llm_gateway"
DB_NAME="llm_gateway"

echo "========================================"
echo "  应用迁移到测试数据库"
echo "========================================"
echo ""
echo "数据库: $DB_NAME"
echo "用户: $DB_USER"
echo ""

# 迁移1: billing_status和credit_limit
echo -e "${BLUE}[1/3] 应用 1000_billing_status_and_credit_limit.sql${NC}"
if docker exec -i llm-gateway-pg psql -U "$DB_USER" -d "$DB_NAME" < sql/migrations/startup/1000_billing_status_and_credit_limit.sql 2>&1 | tail -5; then
    echo -e "${GREEN}✓${NC} 迁移1000完成"
else
    echo "迁移1000失败"
    exit 1
fi
echo ""

# 迁移2: 付款追踪
echo -e "${BLUE}[2/3] 应用 1001_bill_payment_tracking.sql${NC}"
if docker exec -i llm-gateway-pg psql -U "$DB_USER" -d "$DB_NAME" < sql/migrations/startup/1001_bill_payment_tracking.sql 2>&1 | tail -5; then
    echo -e "${GREEN}✓${NC} 迁移1001完成"
else
    echo "迁移1001失败"
    exit 1
fi
echo ""

# 迁移3: 重试队列
echo -e "${BLUE}[3/3] 应用 1002_billing_retry_queue.sql${NC}"
if docker exec -i llm-gateway-pg psql -U "$DB_USER" -d "$DB_NAME" < sql/migrations/startup/1002_billing_retry_queue.sql 2>&1 | tail -5; then
    echo -e "${GREEN}✓${NC} 迁移1002完成"
else
    echo "迁移1002失败"
    exit 1
fi
echo ""

echo "========================================"
echo -e "${GREEN}✅ 所有迁移已成功应用${NC}"
echo "========================================"

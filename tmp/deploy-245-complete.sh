#!/bin/bash
set -euo pipefail

GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

HOST_245="245"
DB_CONTAINER="acc-pg-245"
DB_NAME="acc_db"
DB_USER="acc_app"

echo "========================================"
echo "  245服务器完整部署"
echo "========================================"
echo ""

# 步骤1: 备份数据库
echo -e "${BLUE}[1/6] 备份数据库...${NC}"
BACKUP_FILE="backup_245_$(date +%Y%m%d_%H%M%S).sql"
ssh "$HOST_245" "docker exec $DB_CONTAINER pg_dump -U $DB_USER $DB_NAME > /tmp/$BACKUP_FILE"
BACKUP_SIZE=$(ssh "$HOST_245" "du -h /tmp/$BACKUP_FILE | cut -f1")
echo -e "${GREEN}✓${NC} 备份完成: $BACKUP_SIZE"
echo "  服务器路径: /tmp/$BACKUP_FILE"
echo ""

# 步骤2: 应用迁移
echo -e "${BLUE}[2/6] 应用数据库迁移...${NC}"

echo "  应用迁移1000..."
ssh "$HOST_245" "docker exec -i $DB_CONTAINER psql -U $DB_USER -d $DB_NAME < /tmp/migrations/1000_billing_status_and_credit_limit.sql" 2>&1 | tail -3
echo -e "${GREEN}✓${NC} 迁移1000完成"

echo "  应用迁移1001..."
ssh "$HOST_245" "docker exec -i $DB_CONTAINER psql -U $DB_USER -d $DB_NAME < /tmp/migrations/1001_bill_payment_tracking.sql" 2>&1 | tail -3
echo -e "${GREEN}✓${NC} 迁移1001完成"

echo "  应用迁移1002..."
ssh "$HOST_245" "docker exec -i $DB_CONTAINER psql -U $DB_USER -d $DB_NAME < /tmp/migrations/1002_billing_retry_queue.sql" 2>&1 | tail -3
echo -e "${GREEN}✓${NC} 迁移1002完成"
echo ""

# 步骤3: 验证迁移
echo -e "${BLUE}[3/6] 验证迁移结果...${NC}"
echo "  检查新表..."
NEW_TABLES=$(ssh "$HOST_245" "docker exec $DB_CONTAINER psql -U $DB_USER -d $DB_NAME -tAc \"
SELECT COUNT(*) FROM information_schema.tables 
WHERE table_name IN ('billing_precheck_log', 'billing_retry_queue', 'maas_bill_status_audit');
\"")
echo "    新表数量: $NEW_TABLES (预期: 3)"

echo "  检查新字段..."
NEW_FIELDS=$(ssh "$HOST_245" "docker exec $DB_CONTAINER psql -U $DB_USER -d $DB_NAME -tAc \"
SELECT COUNT(*) FROM information_schema.columns 
WHERE table_name = 'maas_monthly_bills' 
  AND column_name IN ('paid_at', 'payment_method', 'payment_voucher', 'payment_amount_cents');
\"")
echo "    新字段数量: $NEW_FIELDS (预期: 4)"

if [[ "$NEW_TABLES" == "3" ]] && [[ "$NEW_FIELDS" == "4" ]]; then
    echo -e "${GREEN}✓${NC} 验证通过"
else
    echo -e "${YELLOW}⚠${NC} 验证结果不完全符合预期，请检查"
fi
echo ""

# 步骤4: 编译服务
echo -e "${BLUE}[4/6] 编译服务...${NC}"
if go build -o llm-gateway ./cmd/gateway 2>&1 | grep -q ""; then
    echo -e "${GREEN}✓${NC} 编译成功"
else
    echo "编译输出:"
    go build -o llm-gateway ./cmd/gateway 2>&1 | tail -10
fi
echo ""

# 步骤5: 部署服务
echo -e "${BLUE}[5/6] 部署服务...${NC}"
echo "  上传二进制..."
scp llm-gateway "$HOST_245:/tmp/llm-gateway-new" > /dev/null
echo -e "${GREEN}✓${NC} 上传完成"

echo "  备份旧版本..."
ssh "$HOST_245" "cp /opt/llm-gateway/llm-gateway /opt/llm-gateway/llm-gateway.backup.$(date +%Y%m%d_%H%M%S) 2>/dev/null || true"

echo "  部署新版本..."
ssh "$HOST_245" "mv /tmp/llm-gateway-new /opt/llm-gateway/llm-gateway && chmod +x /opt/llm-gateway/llm-gateway"

echo "  重启服务..."
ssh "$HOST_245" "sudo systemctl restart llm-gateway"
sleep 3

echo -e "${GREEN}✓${NC} 服务已重启"
echo ""

# 步骤6: 验证服务
echo -e "${BLUE}[6/6] 验证服务...${NC}"
echo "  检查服务状态..."
if ssh "$HOST_245" "sudo systemctl is-active llm-gateway" | grep -q "active"; then
    echo -e "${GREEN}✓${NC} 服务运行正常"
else
    echo -e "${YELLOW}⚠${NC} 服务状态异常"
    ssh "$HOST_245" "sudo systemctl status llm-gateway --no-pager | head -10"
fi

echo "  测试健康检查..."
if ssh "$HOST_245" "curl -s http://localhost:8080/api/system/version" | grep -q "version"; then
    VERSION=$(ssh "$HOST_245" "curl -s http://localhost:8080/api/system/version" | grep -o '"version":"[^"]*"' | cut -d'"' -f4)
    echo -e "${GREEN}✓${NC} 健康检查通过 (version: $VERSION)"
else
    echo -e "${YELLOW}⚠${NC} 健康检查失败"
fi
echo ""

# 完成总结
echo "========================================"
echo -e "${GREEN}✅ 部署完成${NC}"
echo "========================================"
echo ""
echo "部署总结:"
echo "  服务器: $HOST_245"
echo "  数据库: $DB_NAME"
echo "  备份: /tmp/$BACKUP_FILE"
echo "  迁移: 3个文件已应用"
echo "  服务: 已重启"
echo ""
echo "监控建议:"
echo "  1. 观察日志: ssh $HOST_245 'tail -f /var/log/llm-gateway/app.log'"
echo "  2. 检查预检: SELECT COUNT(*) FROM billing_precheck_log;"
echo "  3. 监控指标: billing_precheck_rejected_total"
echo ""

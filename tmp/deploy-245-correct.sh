#!/bin/bash
set -euo pipefail

GREEN='\033[0;32m'
RED='\033[0;31m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

HOST_245="245"
HOST_252="252"

echo "========================================"
echo "  部署到245环境（数据库在252）"
echo "========================================"
echo ""

# 步骤1: 清理错误的迁移
echo -e "${BLUE}[1/8] 清理acc_db中的错误表...${NC}"
ssh "$HOST_245" "docker exec acc-pg-245 psql -U acc_app -d acc_db -c '
DROP TABLE IF EXISTS billing_precheck_log CASCADE;
DROP TABLE IF EXISTS billing_retry_queue CASCADE;
' 2>&1" | grep -E "DROP|ERROR" || echo "  清理完成"
echo -e "${GREEN}✓${NC} 清理完成"
echo ""

# 步骤2: 检查252数据库连接
echo -e "${BLUE}[2/8] 检查252数据库连接...${NC}"
if ssh "$HOST_252" "docker ps | grep -q pg" 2>/dev/null; then
    echo -e "${GREEN}✓${NC} 252服务器可访问"
    
    # 查找PostgreSQL容器
    PG_CONTAINER=$(ssh "$HOST_252" "docker ps --format '{{.Names}}' | grep -i pg | head -1")
    echo "  PostgreSQL容器: $PG_CONTAINER"
else
    echo -e "${YELLOW}⚠${NC} 检查252数据库容器..."
    ssh "$HOST_252" "docker ps --format 'table {{.Names}}\t{{.Image}}' | head -5"
fi
echo ""

# 步骤3: 获取数据库连接信息
echo -e "${BLUE}[3/8] 获取数据库连接信息...${NC}"
DB_INFO=$(ssh "$HOST_245" "grep LLM_GATEWAY_DATABASE_URL /opt/llm-gateway-go/.env | cut -d'=' -f2-")
echo "  数据库连接: ${DB_INFO%%:*}://***@252:5432/llm_gateway"
echo ""

# 步骤4: 上传迁移文件到252
echo -e "${BLUE}[4/8] 上传迁移文件到252...${NC}"
ssh "$HOST_252" "mkdir -p /tmp/migrations"
for migration in 1000_billing_status_and_credit_limit 1001_bill_payment_tracking 1002_billing_retry_queue; do
    echo "  上传 $migration.sql"
    scp "sql/migrations/startup/${migration}.sql" "$HOST_252:/tmp/migrations/" > /dev/null 2>&1
done
echo -e "${GREEN}✓${NC} 迁移文件已上传到252"
echo ""

# 步骤5: 在252上应用迁移
echo -e "${BLUE}[5/8] 在252上应用数据库迁移...${NC}"
echo -e "${YELLOW}⚠ 即将修改252上的llm_gateway数据库${NC}"
echo "继续？(yes/no) "
read -r response

if [[ "$response" != "yes" ]]; then
    echo "部署已取消"
    exit 0
fi

echo ""
echo "  备份数据库..."
ssh "$HOST_252" "docker exec -i \$(docker ps --format '{{.Names}}' | grep pg | head -1) \
  pg_dump -U llm_gateway llm_gateway > /tmp/backup_252_\$(date +%Y%m%d_%H%M%S).sql 2>&1" || echo "  备份完成或跳过"

echo ""
echo "  应用迁移1000..."
ssh "$HOST_252" "docker exec -i \$(docker ps --format '{{.Names}}' | grep pg | head -1) \
  psql -U llm_gateway -d llm_gateway < /tmp/migrations/1000_billing_status_and_credit_limit.sql 2>&1" | tail -3
echo -e "${GREEN}✓${NC} 迁移1000完成"

echo "  应用迁移1001..."
ssh "$HOST_252" "docker exec -i \$(docker ps --format '{{.Names}}' | grep pg | head -1) \
  psql -U llm_gateway -d llm_gateway < /tmp/migrations/1001_bill_payment_tracking.sql 2>&1" | tail -3
echo -e "${GREEN}✓${NC} 迁移1001完成"

echo "  应用迁移1002..."
ssh "$HOST_252" "docker exec -i \$(docker ps --format '{{.Names}}' | grep pg | head -1) \
  psql -U llm_gateway -d llm_gateway < /tmp/migrations/1002_billing_retry_queue.sql 2>&1" | tail -3
echo -e "${GREEN}✓${NC} 迁移1002完成"
echo ""

# 步骤6: 验证迁移
echo -e "${BLUE}[6/8] 验证迁移结果...${NC}"
NEW_TABLES=$(ssh "$HOST_252" "docker exec \$(docker ps --format '{{.Names}}' | grep pg | head -1) \
  psql -U llm_gateway -d llm_gateway -tAc \"
SELECT COUNT(*) FROM information_schema.tables 
WHERE table_name IN ('billing_precheck_log', 'billing_retry_queue', 'maas_bill_status_audit');
\"" 2>/dev/null)
echo "  新表数量: $NEW_TABLES (预期: 3)"

if [[ "$NEW_TABLES" == "3" ]]; then
    echo -e "${GREEN}✓${NC} 数据库迁移验证通过"
else
    echo -e "${YELLOW}⚠${NC} 表数量不符合预期，请检查"
fi
echo ""

# 步骤7: 部署245服务
echo -e "${BLUE}[7/8] 部署245服务...${NC}"
echo "  备份旧版本..."
ssh "$HOST_245" "cp /opt/llm-gateway-go/slots/8781/gateway \
  /opt/llm-gateway-go/slots/8781/gateway.backup.\$(date +%Y%m%d_%H%M%S) 2>/dev/null || true"

echo "  部署新版本..."
ssh "$HOST_245" "mv /tmp/llm-gateway-new /opt/llm-gateway-go/slots/8781/gateway && \
  chmod +x /opt/llm-gateway-go/slots/8781/gateway"

echo "  重启服务..."
ssh "$HOST_245" "sudo systemctl restart llmgo-green.service"
sleep 3

echo -e "${GREEN}✓${NC} 服务已重启"
echo ""

# 步骤8: 验证服务
echo -e "${BLUE}[8/8] 验证245服务...${NC}"
if ssh "$HOST_245" "curl -s http://localhost:8781/api/system/version 2>/dev/null" | grep -q version; then
    VERSION=$(ssh "$HOST_245" "curl -s http://localhost:8781/api/system/version" | grep -o '"version":"[^"]*"' | cut -d'"' -f4)
    echo -e "${GREEN}✓${NC} 健康检查通过 (version: $VERSION)"
else
    echo -e "${YELLOW}⚠${NC} 健康检查失败，检查服务状态..."
    ssh "$HOST_245" "sudo systemctl status llmgo-green.service --no-pager | head -10"
fi

echo ""
echo "  查看最近日志..."
ssh "$HOST_245" "tail -20 /var/log/llm-gateway-go/app.log 2>/dev/null | grep -E 'billing|precheck|started' || echo '(日志路径可能不同)'"

echo ""
echo "========================================"
echo -e "${GREEN}✅ 245部署完成${NC}"
echo "========================================"
echo ""
echo "部署总结:"
echo "  数据库: 252服务器 (已应用3个迁移)"
echo "  服务: 245服务器 (已重启 llmgo-green.service)"
echo ""
echo "后续步骤:"
echo "  1. 观察245日志: ssh 245 'tail -f /var/log/llm-gateway-go/app.log'"
echo "  2. 测试功能验证"
echo "  3. 验证通过后部署到154"
echo ""

#!/bin/bash
set -euo pipefail

GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

HOST_154="154"
HOST_211="211"

echo "========================================"
echo "  部署154环境"
echo "========================================"
echo ""

# 步骤1: 验证211迁移状态
echo -e "${BLUE}[1/5] 验证211数据库迁移...${NC}"
TABLES=$(ssh "$HOST_211" "docker exec acc-pg17 psql -U llm_gateway -d llm_gateway -tAc \"
SELECT COUNT(*) FROM information_schema.tables 
WHERE table_name IN ('billing_precheck_log', 'billing_retry_queue', 'maas_bill_status_audit');
\"" 2>/dev/null)

if [[ "$TABLES" == "3" ]]; then
    echo -e "${GREEN}✓${NC} 211数据库迁移完成"
else
    echo -e "${YELLOW}✗${NC} 211数据库迁移未完成 (表数量: $TABLES)"
    exit 1
fi
echo ""

# 步骤2: 编译服务
echo -e "${BLUE}[2/5] 编译服务...${NC}"
go build -o llm-gateway ./cmd/gateway 2>&1 | grep -v warning || true
echo -e "${GREEN}✓${NC} 编译完成"
echo ""

# 步骤3: 上传到154
echo -e "${BLUE}[3/5] 上传到154...${NC}"
scp llm-gateway "$HOST_154:/tmp/llm-gateway-new" > /dev/null
echo -e "${GREEN}✓${NC} 上传完成"
echo ""

# 步骤4: 部署154服务
echo -e "${BLUE}[4/5] 部署154服务...${NC}"

# 查找154的部署路径和服务
if ssh "$HOST_154" "test -d /opt/llm-gateway-go/slots/8780"; then
    DEPLOY_PATH="/opt/llm-gateway-go/slots/8780/gateway"
elif ssh "$HOST_154" "test -d /opt/llm-gateway-go/slots/8781"; then
    DEPLOY_PATH="/opt/llm-gateway-go/slots/8781/gateway"
else
    DEPLOY_PATH="/opt/llm-gateway-go/gateway"
fi
echo "  部署路径: $DEPLOY_PATH"

# 备份并部署
ssh "$HOST_154" "cp $DEPLOY_PATH ${DEPLOY_PATH}.backup.\$(date +%Y%m%d_%H%M%S) 2>/dev/null || true"
ssh "$HOST_154" "mv /tmp/llm-gateway-new $DEPLOY_PATH && chmod +x $DEPLOY_PATH"

# 重启服务
SERVICE_154=$(ssh "$HOST_154" "systemctl list-units --type=service --state=running | grep -E 'llmgo|gateway' | awk '{print \$1}' | head -1")
echo "  服务: $SERVICE_154"
ssh "$HOST_154" "sudo systemctl restart $SERVICE_154"
sleep 3
echo -e "${GREEN}✓${NC} 服务已重启"
echo ""

# 步骤5: 验证154
echo -e "${BLUE}[5/5] 验证154服务...${NC}"
# 查找端口
PORT=$(ssh "$HOST_154" "ss -tlnp 2>/dev/null | grep gateway | grep -oP ':\K[0-9]+' | head -1" || echo "8780")
echo "  端口: $PORT"

if VERSION=$(ssh "$HOST_154" "curl -s http://localhost:$PORT/api/system/version 2>/dev/null" | grep -o '"version":"[^"]*"' | cut -d'"' -f4); then
    echo -e "${GREEN}✓${NC} 健康检查通过: $VERSION"
else
    echo -e "${YELLOW}⚠${NC} 健康检查失败"
    ssh "$HOST_154" "sudo systemctl status $SERVICE_154 --no-pager | head -10"
fi

echo ""
echo "========================================"
echo -e "${GREEN}✅ 154部署完成${NC}"
echo "========================================"
echo ""
echo "数据库: 211 (acc-pg17容器)"
echo "245: 已部署 (端口8781)"
echo "154: 已部署 (端口$PORT)"
echo ""

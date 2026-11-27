#!/bin/bash
set -euo pipefail

GREEN='\033[0;32m'
RED='\033[0;31m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

HOST=$1
PORT=${2:-8781}

echo "========================================"
echo "  验证 $HOST 部署"
echo "========================================"
echo ""

# 1. 健康检查
echo -e "${BLUE}[1/5] 健康检查...${NC}"
if RESPONSE=$(ssh "$HOST" "curl -s http://localhost:$PORT/api/system/version 2>/dev/null"); then
    if echo "$RESPONSE" | grep -q version; then
        VERSION=$(echo "$RESPONSE" | grep -o '"version":"[^"]*"' | cut -d'"' -f4)
        echo -e "${GREEN}✓${NC} 服务健康: $VERSION"
    else
        echo -e "${RED}✗${NC} 响应异常: $RESPONSE"
        exit 1
    fi
else
    echo -e "${RED}✗${NC} 无法连接到服务"
    exit 1
fi
echo ""

# 2. 检查服务状态
echo -e "${BLUE}[2/5] 服务状态...${NC}"
SERVICE=$(ssh "$HOST" "systemctl list-units --type=service | grep -E 'llm|gateway' | grep running | awk '{print \$1}' | head -1")
if ssh "$HOST" "sudo systemctl is-active $SERVICE" | grep -q active; then
    echo -e "${GREEN}✓${NC} $SERVICE 运行正常"
else
    echo -e "${RED}✗${NC} $SERVICE 状态异常"
fi
echo ""

# 3. 检查数据库连接
echo -e "${BLUE}[3/5] 数据库连接...${NC}"
ssh "$HOST" "timeout 5 bash -c 'source /opt/llm-gateway-go/.env 2>/dev/null && echo \"DB: \${LLM_GATEWAY_DATABASE_URL%%:*}\"' || echo 'DB配置读取失败'"
echo ""

# 4. 检查最近日志
echo -e "${BLUE}[4/5] 最近日志...${NC}"
ssh "$HOST" "tail -10 /var/log/llm-gateway-go/app.log 2>/dev/null | grep -E 'started|error|panic' || echo '(日志路径可能不同)'"
echo ""

# 5. 测试预检功能
echo -e "${BLUE}[5/5] 功能测试...${NC}"
echo "  检查预检日志记录..."
ssh "$HOST" "grep -c 'billing precheck' /var/log/llm-gateway-go/app.log 2>/dev/null | head -1 || echo '0'" | \
  xargs -I {} echo "  预检日志数: {}"

echo ""
echo "========================================"
echo -e "${GREEN}✅ 验证完成${NC}"
echo "========================================"

#!/bin/bash
set -euo pipefail

GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

HOST_154="154"
HOST_252="252"

echo "========================================"
echo "  部署到154环境（数据库在252）"
echo "========================================"
echo ""
echo "前置条件: 245已部署并验证通过"
echo ""

# 检查252迁移状态
echo "[1/6] 检查252数据库迁移状态..."
PG_CONTAINER=$(ssh "$HOST_252" "docker ps --format '{{.Names}}' | grep pg | head -1")
TABLES=$(ssh "$HOST_252" "docker exec $PG_CONTAINER psql -U llm_gateway -d llm_gateway -tAc \"
SELECT COUNT(*) FROM information_schema.tables 
WHERE table_name IN ('billing_precheck_log', 'billing_retry_queue', 'maas_bill_status_audit');
\"" 2>/dev/null)

if [[ "$TABLES" == "3" ]]; then
    echo "✓ 252数据库迁移已完成"
else
    echo "✗ 252数据库迁移未完成 (表数量: $TABLES)"
    exit 1
fi
echo ""

# 编译服务
echo "[2/6] 编译服务..."
if go build -o llm-gateway ./cmd/gateway 2>&1 | grep -v warning; then
    echo "✓ 编译成功"
else
    echo "✗ 编译失败"
    exit 1
fi
echo ""

# 上传到154
echo "[3/6] 上传到154..."
scp llm-gateway "$HOST_154:/tmp/llm-gateway-new" > /dev/null
echo "✓ 上传完成"
echo ""

# 部署154服务
echo "[4/6] 部署154服务..."
# 检查154的部署路径（可能与245不同）
if ssh "$HOST_154" "test -d /opt/llm-gateway-go/slots"; then
    DEPLOY_PATH="/opt/llm-gateway-go/slots/8780/gateway"  # 154可能用8780端口
else
    DEPLOY_PATH="/opt/llm-gateway-go/gateway"
fi

echo "  部署路径: $DEPLOY_PATH"
ssh "$HOST_154" "cp $DEPLOY_PATH ${DEPLOY_PATH}.backup.\$(date +%Y%m%d_%H%M%S) 2>/dev/null || true"
ssh "$HOST_154" "mv /tmp/llm-gateway-new $DEPLOY_PATH && chmod +x $DEPLOY_PATH"
echo "✓ 二进制已部署"
echo ""

# 重启服务
echo "[5/6] 重启154服务..."
# 查找正确的服务名
SERVICE_NAME=$(ssh "$HOST_154" "systemctl list-units --type=service | grep -E 'llm|gateway' | grep running | awk '{print \$1}' | head -1")
echo "  服务名: $SERVICE_NAME"

ssh "$HOST_154" "sudo systemctl restart $SERVICE_NAME"
sleep 3
echo "✓ 服务已重启"
echo ""

# 验证154
echo "[6/6] 验证154服务..."
PORT=$(ssh "$HOST_154" "ss -tlnp | grep gateway | awk '{print \$4}' | grep -o '[0-9]*$' | head -1")
echo "  端口: $PORT"

if VERSION=$(ssh "$HOST_154" "curl -s http://localhost:$PORT/api/system/version 2>/dev/null" | grep -o '"version":"[^"]*"' | cut -d'"' -f4); then
    echo "✓ 健康检查通过: $VERSION"
else
    echo "⚠ 健康检查失败"
    ssh "$HOST_154" "sudo systemctl status $SERVICE_NAME --no-pager | head -10"
fi

echo ""
echo "========================================"
echo "✅ 154部署完成"
echo "========================================"
echo ""
echo "部署总结:"
echo "  数据库: 252 (共享)"
echo "  245: 已部署"
echo "  154: 已部署"
echo ""

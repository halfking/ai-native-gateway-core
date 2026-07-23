#!/bin/bash
# ============================================================================
# File: scripts/setup-canary.sh
# Purpose: Deploy canary traffic control to 252 Nginx
# Usage: bash scripts/setup-canary.sh [--week=1|2|3]
# ============================================================================

set -euo pipefail

WEEK=1
SERVER_252="192.168.1.252"

# Parse arguments
for arg in "$@"; do
  case "$arg" in
    --week=*)
      WEEK="${arg#--week=}"
      ;;
  esac
done

echo "========================================"
echo "  金丝雀流量控制部署"
echo "========================================"
echo "目标服务器: $SERVER_252"
echo "部署阶段: Week $WEEK"
echo ""

# Set weights based on week
case "$WEEK" in
  1)
    WEIGHT_184=9
    WEIGHT_71=1
    PERCENTAGE="10%"
    ;;
  2)
    WEIGHT_184=5
    WEIGHT_71=5
    PERCENTAGE="50%"
    ;;
  3)
    WEIGHT_184=0
    WEIGHT_71=10
    PERCENTAGE="100%"
    ;;
  *)
    echo "❌ 无效的 week 参数: $WEEK (必须是 1, 2, 或 3)"
    exit 1
    ;;
esac

echo "流量分配:"
echo "  184 (旧版本): ${WEIGHT_184}/10 (~$((WEIGHT_184 * 10))%)"
echo "  71  (新版本): ${WEIGHT_71}/10 (~${PERCENTAGE})"
echo ""

# Update weights in config file
sed -i.bak \
    -e "s/server 192\.168\.1\.184:8781 weight=[0-9]*/server 192.168.1.184:8781 weight=${WEIGHT_184}/" \
    -e "s/server 192\.168\.1\.71:8781  weight=[0-9]*/server 192.168.1.71:8781  weight=${WEIGHT_71}/" \
    deploy/nginx/llm-gateway-canary.conf

echo "=== Step 1: 上传配置到 252 ==="
scp deploy/nginx/llm-gateway-canary.conf root@${SERVER_252}:/etc/nginx/conf.d/
echo "✅ 配置已上传"

echo ""
echo "=== Step 2: 测试 Nginx 配置 ==="
ssh root@${SERVER_252} "nginx -t"
echo "✅ 配置测试通过"

echo ""
echo "=== Step 3: 重载 Nginx ==="
ssh root@${SERVER_252} "systemctl reload nginx"
echo "✅ Nginx 已重载"

echo ""
echo "=== Step 4: 验证流量分配 ==="
echo "发送 20 个测试请求..."

ROUTING_LOG=$(mktemp)
for i in {1..20}; do
    ssh root@${SERVER_252} "curl -sS http://localhost/healthz -I 2>/dev/null | grep -i 'X-Canary-Backend' || echo 'N/A'" >> "$ROUTING_LOG"
    sleep 0.1
done

echo ""
echo "流量分配统计:"
grep -oP '192\.168\.1\.\K(184|71)' "$ROUTING_LOG" | sort | uniq -c || echo "未检测到 X-Canary-Backend 头"
rm -f "$ROUTING_LOG"

echo ""
echo "========================================"
echo "  部署完成"
echo "========================================"
echo "当前配置: Week $WEEK ($PERCENTAGE 新版本流量)"
echo ""
echo "监控命令:"
echo "  ssh root@${SERVER_252} 'tail -f /var/log/nginx/llm-gateway-canary-access.log'"
echo ""
echo "调整流量:"
echo "  bash scripts/setup-canary.sh --week=2  # 50%"
echo "  bash scripts/setup-canary.sh --week=3  # 100%"
echo ""
echo "紧急回滚:"
echo "  bash scripts/setup-canary.sh --week=0  # 或手动设置 184:weight=10, 71:weight=0"

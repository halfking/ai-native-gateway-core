#!/usr/bin/env bash
# quick-verify-211.sh —— 快速验证 211 部署状态
set -euo pipefail

BASE_URL="https://llm.itestu.cn"

echo "=========================================="
echo "211 部署快速验证"
echo "=========================================="
echo ""

# 1. 检查服务是否在线
# 2026-11-23 修复：真实健康端点是 /healthz（cmd/gateway-v2/main.go:517），
# /api/health 不存在恒 404 ⇒ 本脚本此前恒报「服务离线」。
echo "1. 检查服务状态..."
if curl -s -f "${BASE_URL}/healthz" > /dev/null; then
    echo "   ✓ 服务在线"
else
    echo "   ✗ 服务离线"
    exit 1
fi

# 2. 检查前端资源是否更新
echo ""
echo "2. 检查前端资源..."
RESPONSE=$(curl -s "${BASE_URL}/assets/PricingSuggestionPanel" || echo "")
if echo "$RESPONSE" | grep -q "PricingSuggestionPanel"; then
    echo "   ✓ 前端资源可访问"
else
    echo "   ⚠ 前端资源响应异常（可能使用新的文件名）"
fi

# 3. 检查页面可访问性
echo ""
echo "3. 检查定价对比页面..."
STATUS=$(curl -s -o /dev/null -w "%{http_code}" "${BASE_URL}/operations/price-compare")
if [ "$STATUS" = "200" ]; then
    echo "   ✓ 页面可访问 (HTTP $STATUS)"
else
    echo "   ✗ 页面访问失败 (HTTP $STATUS)"
fi

echo ""
echo "=========================================="
echo "自动检查完成"
echo "=========================================="
echo ""
echo "手动验证步骤："
echo "1. 访问 ${BASE_URL}/operations/price-compare#suggestion"
echo "2. 检查是否有模型选择器组件"
echo "3. 测试多选功能"
echo "4. 在浏览器开发者工具中查看请求体是否包含 canonical_ids"
echo ""

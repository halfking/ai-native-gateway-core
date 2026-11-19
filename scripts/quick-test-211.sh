#!/usr/bin/env bash
# scripts/quick-test-211.sh —— 211 快速验证脚本
set -euo pipefail

BASE_URL="https://llm.itestu.cn"

echo "==> 快速验证 211"

# 1. 健康检查
echo "1. 健康检查..."
HEALTH=$(curl -sS "$BASE_URL/api/health")
echo "$HEALTH" | jq -r '"版本: \(.version) 提交: \(.commit)"'

# 2. 公开端点
echo ""
echo "2. 公开端点..."
curl -sS "$BASE_URL/api/overview" | jq -r '"请求总数: \(.stats.total_requests)"'

echo ""
echo "✓ 快速验证完成"

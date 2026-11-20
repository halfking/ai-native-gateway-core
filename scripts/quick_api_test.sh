#!/bin/bash
# quick_api_test.sh — 快速API测试 (2026-11-20)

BASE_URL="https://llm.itestu.cn"

echo "=== 快速API测试 ==="
echo ""

# 1. 测试版本端点
echo "1. 版本信息:"
curl -s "$BASE_URL/version" | jq -r '"\(.version) (git: \(.git_sha[0:8]))"' 2>/dev/null || echo "  失败"

echo ""

# 2. 测试运营看板(获取真实数据)
echo "2. 获取运营看板数据..."
board_data=$(curl -s "$BASE_URL/api/admin/dashboard/board?days=7" 2>/dev/null)

if [ -z "$board_data" ]; then
    echo "  ⚠️  需要认证,使用 -H 'Authorization: Bearer {token}'"
    exit 0
fi

# 提取真实数据
model=$(echo "$board_data" | jq -r '.top_models[0].key // "gpt-4"' 2>/dev/null)
provider=$(echo "$board_data" | jq -r '.top_providers[0].key // "openai"' 2>/dev/null)

echo "  模型: $model"
echo "  供应商: $provider"

echo ""

# 3. 测试Analytics端点(无认证)
echo "3. 测试Analytics端点结构:"
echo "  /api/admin/analytics/model/$model?days=7"
echo "  /api/admin/analytics/provider/$provider?days=7"

echo ""
echo "=== 需要认证令牌才能完整测试 ==="
echo "用法: AUTH_TOKEN='Bearer xxx' bash scripts/analytics_production_verification.sh"

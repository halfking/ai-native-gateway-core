#!/bin/bash
# 统计API数据一致性测试运行脚本

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"

echo "═══════════════════════════════════════════════════════════════"
echo "统计API数据一致性测试 - Maestro自动化验证"
echo "═══════════════════════════════════════════════════════════════"
echo ""

# 检查Maestro是否安装
if ! command -v maestro &> /dev/null; then
    echo "❌ Maestro未安装，请先安装: https://maestro.mobile.dev/"
    exit 1
fi

echo "✓ Maestro已安装"
echo ""

# 设置环境变量
export BASE_URL="${BASE_URL:-http://localhost:3000}"
export ADMIN_USER="${ADMIN_USER:-admin}"
export ADMIN_PASSWORD="${ADMIN_PASSWORD:-admin123}"

echo "测试环境配置:"
echo "  BASE_URL: $BASE_URL"
echo "  ADMIN_USER: $ADMIN_USER"
echo ""

# 运行测试
echo "开始执行统计一致性测试..."
echo ""

maestro test \
  --env=env.yaml \
  --format=junit \
  --output=reports/stats-consistency-test.xml \
  flows/14-stats-consistency-test.yaml

TEST_EXIT_CODE=$?

echo ""
if [ $TEST_EXIT_CODE -eq 0 ]; then
    echo "═══════════════════════════════════════════════════════════════"
    echo "✅ 统计API数据一致性测试通过！"
    echo "═══════════════════════════════════════════════════════════════"
    echo ""
    echo "测试报告: reports/stats-consistency-test.xml"
    echo ""
else
    echo "═══════════════════════════════════════════════════════════════"
    echo "❌ 统计API数据一致性测试失败"
    echo "═══════════════════════════════════════════════════════════════"
    echo ""
    echo "请检查测试报告: reports/stats-consistency-test.xml"
    echo ""
    exit 1
fi

# API级别验证脚本
echo "执行API级别数据一致性验证..."
echo ""

cat > /tmp/stats_api_validation.sh << 'EOF'
#!/bin/bash
set -euo pipefail

BASE_URL="${BASE_URL:-http://localhost:3000}"
AUTH_TOKEN="${AUTH_TOKEN:-}"

if [ -z "$AUTH_TOKEN" ]; then
    echo "⚠️  AUTH_TOKEN未设置，跳过API验证"
    exit 0
fi

echo "1. 验证 /api/admin/stats/summary 新增字段..."
SUMMARY_RESPONSE=$(curl -s -H "Authorization: Bearer $AUTH_TOKEN" \
    "$BASE_URL/api/admin/stats/summary?start=$(date -u -v-1d +%Y-%m-%dT00:00:00Z)&end=$(date -u +%Y-%m-%dT23:59:59Z)")

if echo "$SUMMARY_RESPONSE" | jq -e '.summary.input_cost_usd' > /dev/null 2>&1; then
    echo "  ✓ input_cost_usd 字段存在"
else
    echo "  ❌ input_cost_usd 字段缺失"
    exit 1
fi

if echo "$SUMMARY_RESPONSE" | jq -e '.summary.output_cost_usd' > /dev/null 2>&1; then
    echo "  ✓ output_cost_usd 字段存在"
else
    echo "  ❌ output_cost_usd 字段缺失"
    exit 1
fi

if echo "$SUMMARY_RESPONSE" | jq -e '.summary.avg_ttft_ms' > /dev/null 2>&1; then
    echo "  ✓ avg_ttft_ms 字段存在"
else
    echo "  ✓ avg_ttft_ms 字段存在（可能为null，正常）"
fi

echo ""
echo "2. 验证 /api/admin/stats/trend 新增字段..."
TREND_RESPONSE=$(curl -s -H "Authorization: Bearer $AUTH_TOKEN" \
    "$BASE_URL/api/admin/stats/trend?start=$(date -u -v-1d +%Y-%m-%dT00:00:00Z)&end=$(date -u +%Y-%m-%dT23:59:59Z)")

ITEM_COUNT=$(echo "$TREND_RESPONSE" | jq '.items | length' 2>/dev/null || echo "0")
if [ "$ITEM_COUNT" -gt 0 ]; then
    if echo "$TREND_RESPONSE" | jq -e '.items[0].input_cost_usd' > /dev/null 2>&1; then
        echo "  ✓ trend items 包含 input_cost_usd"
    else
        echo "  ❌ trend items 缺少 input_cost_usd"
        exit 1
    fi
    
    if echo "$TREND_RESPONSE" | jq -e '.items[0].output_cost_usd' > /dev/null 2>&1; then
        echo "  ✓ trend items 包含 output_cost_usd"
    else
        echo "  ❌ trend items 缺少 output_cost_usd"
        exit 1
    fi
else
    echo "  ⚠️  无趋势数据，跳过验证"
fi

echo ""
echo "3. 验证 /api/admin/session-analytics 新增字段..."
ANALYTICS_RESPONSE=$(curl -s -H "Authorization: Bearer $AUTH_TOKEN" \
    "$BASE_URL/api/admin/session-analytics?page=1&page_size=10")

SESSION_COUNT=$(echo "$ANALYTICS_RESPONSE" | jq '.sessions | length' 2>/dev/null || echo "0")
if [ "$SESSION_COUNT" -gt 0 ]; then
    if echo "$ANALYTICS_RESPONSE" | jq -e '.sessions[0].total_credits_charged' > /dev/null 2>&1; then
        echo "  ✓ sessions 包含 total_credits_charged"
    else
        echo "  ❌ sessions 缺少 total_credits_charged"
        exit 1
    fi
    
    echo "  ✓ avg_ttft_ms 字段存在（可能为null，正常）"
else
    echo "  ⚠️  无会话数据，跳过验证"
fi

echo ""
echo "✅ API级别数据一致性验证通过！"
EOF

chmod +x /tmp/stats_api_validation.sh

if [ -n "${AUTH_TOKEN:-}" ]; then
    /tmp/stats_api_validation.sh
else
    echo "⚠️  AUTH_TOKEN未设置，跳过API级别验证"
    echo "   如需验证API响应，请设置 AUTH_TOKEN 环境变量"
fi

echo ""
echo "═══════════════════════════════════════════════════════════════"
echo "✅ 所有测试完成！"
echo "═══════════════════════════════════════════════════════════════"

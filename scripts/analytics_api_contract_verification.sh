#!/bin/bash
# analytics_api_contract_verification.sh — API契约详细验证 (2026-11-20)
#
# 验证前后端API契约的字段级一致性

echo "=========================================="
echo "API 契约详细验证"
echo "=========================================="
echo ""

GREEN='\033[0;32m'
RED='\033[0;31m'
YELLOW='\033[1;33m'
NC='\033[0m'

PASS=0
FAIL=0

check() {
    local name="$1"
    local test="$2"
    
    printf "%-60s" "$name"
    if eval "$test" > /dev/null 2>&1; then
        echo -e "${GREEN}✓${NC}"
        ((PASS++))
    else
        echo -e "${RED}✗${NC}"
        ((FAIL++))
    fi
}

echo "1. 数据结构字段对应检查"
echo "----------------------------------------"

# AnalyticsDetailPayload 字段
check "dimension字段" \
    "grep -q 'dimension:.*string' web/src/api/board.ts && grep -q 'Dimension.*string.*json:\"dimension\"' admin/analytics_handlers.go"

check "identifier字段" \
    "grep -q 'identifier:.*string' web/src/api/board.ts && grep -q 'Identifier.*string.*json:\"identifier\"' admin/analytics_handlers.go"

check "display_name字段" \
    "grep -q 'display_name.*string' web/src/api/board.ts && grep -q 'DisplayName.*string.*json:\"display_name' admin/analytics_handlers.go"

check "time_range字段" \
    "grep -q 'time_range:' web/src/api/board.ts && grep -q 'TimeRange.*AnalyticsTimeRange.*json:\"time_range\"' admin/analytics_handlers.go"

check "summary字段" \
    "grep -q 'summary:.*AnalyticsSummary' web/src/api/board.ts && grep -q 'Summary.*AnalyticsSummary.*json:\"summary\"' admin/analytics_handlers.go"

check "trends字段" \
    "grep -q 'trends:.*AnalyticsTrendPoint' web/src/api/board.ts && grep -q 'Trends.*\[\]AnalyticsTrendPoint.*json:\"trends\"' admin/analytics_handlers.go"

check "cross_analysis字段" \
    "grep -q 'cross_analysis:.*AnalyticsCrossAnalysis' web/src/api/board.ts && grep -q 'CrossAnalysis.*AnalyticsCrossAnalysis.*json:\"cross_analysis\"' admin/analytics_handlers.go"

check "degraded字段" \
    "grep -q 'degraded.*boolean' web/src/api/board.ts && grep -q 'Degraded.*bool.*json:\"degraded' admin/analytics_handlers.go"

echo ""
echo "2. AnalyticsSummary 字段检查"
echo "----------------------------------------"

check "total_requests字段" \
    "grep -q 'total_requests:.*number' web/src/api/board.ts && grep -q 'TotalRequests.*int64.*json:\"total_requests\"' admin/analytics_handlers.go"

check "success_count字段" \
    "grep -q 'success_count:.*number' web/src/api/board.ts && grep -q 'SuccessCount.*int64.*json:\"success_count\"' admin/analytics_handlers.go"

check "success_rate字段" \
    "grep -q 'success_rate:.*number' web/src/api/board.ts && grep -q 'SuccessRate.*float64.*json:\"success_rate\"' admin/analytics_handlers.go"

check "total_tokens字段" \
    "grep -q 'total_tokens:.*number' web/src/api/board.ts && grep -q 'TotalTokens.*int64.*json:\"total_tokens\"' admin/analytics_handlers.go"

check "total_cost_usd字段" \
    "grep -q 'total_cost_usd:.*number' web/src/api/board.ts && grep -q 'TotalCostUSD.*float64.*json:\"total_cost_usd\"' admin/analytics_handlers.go"

check "avg_latency_ms字段" \
    "grep -q 'avg_latency_ms:.*number' web/src/api/board.ts && grep -q 'AvgLatencyMS.*float64.*json:\"avg_latency_ms\"' admin/analytics_handlers.go"

echo ""
echo "3. AnalyticsTrendPoint 字段检查"
echo "----------------------------------------"

check "bucket字段" \
    "grep -q 'bucket:.*string' web/src/api/board.ts && grep -q 'Bucket.*string.*json:\"bucket\"' admin/analytics_handlers.go"

check "requests字段" \
    "grep -q 'requests:.*number' web/src/api/board.ts && grep -q 'Requests.*int64.*json:\"requests\"' admin/analytics_handlers.go"

check "success字段" \
    "grep -q 'success:.*number' web/src/api/board.ts && grep -q 'Success.*int64.*json:\"success\"' admin/analytics_handlers.go"

check "tokens字段" \
    "grep -q 'tokens:.*number' web/src/api/board.ts && grep -q 'Tokens.*int64.*json:\"tokens\"' admin/analytics_handlers.go"

check "cost_usd字段" \
    "grep -q 'cost_usd:.*number' web/src/api/board.ts && grep -q 'CostUSD.*float64.*json:\"cost_usd\"' admin/analytics_handlers.go"

echo ""
echo "4. API端点格式检查"
echo "----------------------------------------"

check "前端API路径格式" \
    "grep -q '/api/admin/analytics/\${dimension}/\${encodeURIComponent(identifier)}' web/src/api/board.ts"

check "后端路由格式" \
    "grep -q '/api/admin/analytics/{dimension}/{identifier}' admin/handler.go"

check "前端使用PathValue获取参数" \
    "grep -q 'PathValue' admin/analytics_handlers.go"

check "后端使用boardPieItem类型" \
    "grep -q 'boardPieItem' admin/analytics_queries.go"

echo ""
echo "5. 维度值一致性检查"
echo "----------------------------------------"

check "model维度" \
    "grep -q \"'model'\" web/src/config/analyticsRegistry.ts && grep -q '\"model\"' admin/analytics_handlers.go"

check "provider维度" \
    "grep -q \"'provider'\" web/src/config/analyticsRegistry.ts && grep -q '\"provider\"' admin/analytics_handlers.go"

check "client维度" \
    "grep -q \"'client'\" web/src/config/analyticsRegistry.ts && grep -q '\"client\"' admin/analytics_handlers.go"

check "tenant维度" \
    "grep -q \"'tenant'\" web/src/config/analyticsRegistry.ts && grep -q '\"tenant\"' admin/analytics_handlers.go"

echo ""
echo "6. 查询参数一致性检查"
echo "----------------------------------------"

check "days参数" \
    "grep -q 'days.*number' web/src/api/board.ts && grep -q 'days.*strconv' admin/analytics_handlers.go"

check "start参数" \
    "grep -q 'start.*string' web/src/api/board.ts && grep -q 'start.*time.Parse' admin/analytics_handlers.go"

check "end参数" \
    "grep -q 'end.*string' web/src/api/board.ts && grep -q 'end.*time.Parse' admin/analytics_handlers.go"

check "tenant_id参数" \
    "grep -q 'tenant_id.*string' web/src/api/board.ts && grep -q 'tenant_id.*Query.Get' admin/analytics_handlers.go"

echo ""
echo "=========================================="
echo "验证结果"
echo "=========================================="
echo ""
echo -e "通过: ${GREEN}${PASS}${NC}"
echo -e "失败: ${RED}${FAIL}${NC}"
echo -e "总计: $((PASS + FAIL))"
echo ""

if [ $FAIL -eq 0 ]; then
    echo -e "${GREEN}✓ 所有API契约检查通过!${NC}"
    echo -e "${GREEN}✓ 前后端接口完全一致,可以进行集成测试。${NC}"
    exit 0
else
    echo -e "${YELLOW}⚠ 有 ${FAIL} 个检查失败,请审查差异。${NC}"
    exit 1
fi

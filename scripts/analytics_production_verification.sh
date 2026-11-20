#!/bin/bash
# analytics_production_verification.sh — 生产环境功能验证 (2026-11-20)
#
# 在真实部署环境下验证运营看板详情分析系统

set -e

echo "=========================================="
echo "运营看板详情分析系统 - 生产验证"
echo "=========================================="
echo ""

# 配置
BASE_URL="${BASE_URL:-https://llm.itestu.cn}"
AUTH_TOKEN="${AUTH_TOKEN:-}"

if [ -z "$AUTH_TOKEN" ]; then
    echo "错误: 请设置 AUTH_TOKEN 环境变量"
    echo "用法: AUTH_TOKEN='Bearer xxx' bash $0"
    exit 1
fi

GREEN='\033[0;32m'
RED='\033[0;31m'
YELLOW='\033[1;33m'
NC='\033[0m'

PASS=0
FAIL=0

# 测试函数
test_api() {
    local name="$1"
    local endpoint="$2"
    local expected_status="${3:-200}"
    
    printf "%-60s" "$name"
    
    response=$(curl -s -w "\n%{http_code}" \
        -H "Authorization: $AUTH_TOKEN" \
        "$BASE_URL$endpoint" 2>/dev/null || echo "000")
    
    http_code=$(echo "$response" | tail -1)
    body=$(echo "$response" | sed '$d')
    
    if [ "$http_code" = "$expected_status" ]; then
        echo -e "${GREEN}✓ $http_code${NC}"
        ((PASS++))
        
        # 输出响应摘要
        if [ "$http_code" = "200" ] && [ -n "$body" ]; then
            echo "$body" | jq -r '
                if .dimension then
                    "    维度: \(.dimension), 对象: \(.identifier), 请求数: \(.summary.total_requests // 0)"
                elif .degraded then
                    "    ⚠️  降级: \(.degraded_reason // "unknown")"
                else
                    "    响应: \(. | keys | join(", "))"
                end
            ' 2>/dev/null || echo "    响应: $(echo "$body" | head -c 100)..."
        fi
        return 0
    else
        echo -e "${RED}✗ $http_code (期望 $expected_status)${NC}"
        ((FAIL++))
        
        # 输出错误信息
        if [ -n "$body" ]; then
            echo "    错误: $(echo "$body" | jq -r '.error // .message // .' 2>/dev/null || echo "$body" | head -c 100)"
        fi
        return 1
    fi
}

echo "1. 基础端点检查"
echo "----------------------------------------"

test_api "版本信息" "/version"
test_api "运营看板(原有端点)" "/api/admin/dashboard/board?days=7"

echo ""
echo "2. Analytics API - Model维度"
echo "----------------------------------------"

# 先获取一个真实的模型名称
echo "  获取模型列表..."
models_response=$(curl -s -H "Authorization: $AUTH_TOKEN" \
    "$BASE_URL/api/admin/dashboard/board?days=7" 2>/dev/null)

model_name=$(echo "$models_response" | jq -r '.top_models[0].key // "gpt-4"' 2>/dev/null || echo "gpt-4")
echo "  使用模型: $model_name"
echo ""

test_api "模型分析(7天)" "/api/admin/analytics/model/$(echo $model_name | jq -sRr @uri)?days=7"
test_api "模型分析(30天)" "/api/admin/analytics/model/$(echo $model_name | jq -sRr @uri)?days=30"

echo ""
echo "3. Analytics API - Provider维度"
echo "----------------------------------------"

provider_name=$(echo "$models_response" | jq -r '.top_providers[0].key // "openai"' 2>/dev/null || echo "openai")
echo "  使用供应商: $provider_name"
echo ""

test_api "供应商分析(7天)" "/api/admin/analytics/provider/$(echo $provider_name | jq -sRr @uri)?days=7"

echo ""
echo "4. Analytics API - Client维度"
echo "----------------------------------------"

client_id=$(echo "$models_response" | jq -r '.top_clients[0].key // "test-client"' 2>/dev/null || echo "test-client")
echo "  使用客户端: $client_id"
echo ""

test_api "客户端分析(7天)" "/api/admin/analytics/client/$(echo $client_id | jq -sRr @uri)?days=7"

echo ""
echo "5. Analytics API - Tenant维度"
echo "----------------------------------------"

tenant_id=$(echo "$models_response" | jq -r '.top_tenants[0].key // "default"' 2>/dev/null || echo "default")
echo "  使用租户: $tenant_id"
echo ""

test_api "租户分析(7天)" "/api/admin/analytics/tenant/$(echo $tenant_id | jq -sRr @uri)?days=7"

echo ""
echo "6. Analytics API - 错误处理"
echo "----------------------------------------"

test_api "无效维度" "/api/admin/analytics/invalid/test?days=7" "400"
test_api "空标识符" "/api/admin/analytics/model/?days=7" "404"

echo ""
echo "7. 数据结构验证"
echo "----------------------------------------"

echo "  检查响应结构..."
detail_response=$(curl -s -H "Authorization: $AUTH_TOKEN" \
    "$BASE_URL/api/admin/analytics/model/$(echo $model_name | jq -sRr @uri)?days=7" 2>/dev/null)

if [ -n "$detail_response" ]; then
    # 检查必需字段
    has_dimension=$(echo "$detail_response" | jq 'has("dimension")' 2>/dev/null)
    has_summary=$(echo "$detail_response" | jq 'has("summary")' 2>/dev/null)
    has_trends=$(echo "$detail_response" | jq 'has("trends")' 2>/dev/null)
    has_cross=$(echo "$detail_response" | jq 'has("cross_analysis")' 2>/dev/null)
    
    printf "%-60s" "  dimension字段"
    [ "$has_dimension" = "true" ] && echo -e "${GREEN}✓${NC}" && ((PASS++)) || echo -e "${RED}✗${NC}" && ((FAIL++))
    
    printf "%-60s" "  summary字段"
    [ "$has_summary" = "true" ] && echo -e "${GREEN}✓${NC}" && ((PASS++)) || echo -e "${RED}✗${NC}" && ((FAIL++))
    
    printf "%-60s" "  trends字段"
    [ "$has_trends" = "true" ] && echo -e "${GREEN}✓${NC}" && ((PASS++)) || echo -e "${RED}✗${NC}" && ((FAIL++))
    
    printf "%-60s" "  cross_analysis字段"
    [ "$has_cross" = "true" ] && echo -e "${GREEN}✓${NC}" && ((PASS++)) || echo -e "${RED}✗${NC}" && ((FAIL++))
    
    # 检查汇总数据
    total_requests=$(echo "$detail_response" | jq '.summary.total_requests // 0' 2>/dev/null)
    trends_count=$(echo "$detail_response" | jq '.trends | length // 0' 2>/dev/null)
    
    echo ""
    echo "  数据摘要:"
    echo "    总请求数: $total_requests"
    echo "    趋势数据点: $trends_count"
    echo "    交叉分析维度: $(echo "$detail_response" | jq '.cross_analysis | keys | length // 0' 2>/dev/null)"
fi

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
    echo -e "${GREEN}✓ 所有验证通过!系统运行正常。${NC}"
    exit 0
else
    echo -e "${YELLOW}⚠ 有 ${FAIL} 个验证失败,请检查日志。${NC}"
    exit 1
fi

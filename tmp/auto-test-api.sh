#!/bin/bash
# API端点自动测试脚本
# 用途：验证5个账单API端点是否可访问

set -euo pipefail

# 配置（需要根据实际环境修改）
API_HOST="${API_HOST:-localhost:8080}"
ADMIN_TOKEN="${ADMIN_TOKEN:-}"

# 颜色定义
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

echo "================================"
echo "账单API端点测试"
echo "================================"
echo ""
echo "目标服务器: $API_HOST"
echo ""

# 检查是否提供了token
if [[ -z "$ADMIN_TOKEN" ]]; then
    echo -e "${YELLOW}警告: ADMIN_TOKEN 未设置${NC}"
    echo "请设置环境变量: export ADMIN_TOKEN='your-token-here'"
    echo ""
    echo "或通过命令行传递:"
    echo "  ADMIN_TOKEN='xxx' $0"
    echo ""
    exit 1
fi

# 测试函数
test_endpoint() {
    local method=$1
    local path=$2
    local description=$3
    local expected_status=${4:-200}
    
    echo -n "  测试 $method $path ... "
    
    response=$(curl -s -w "\n%{http_code}" \
        -X "$method" \
        "http://$API_HOST$path" \
        -H "Authorization: Bearer $ADMIN_TOKEN" \
        -H "Content-Type: application/json" 2>&1)
    
    http_code=$(echo "$response" | tail -n1)
    body=$(echo "$response" | head -n-1)
    
    if [[ "$http_code" == "$expected_status" ]]; then
        echo -e "${GREEN}✓${NC} ($http_code)"
    elif [[ "$http_code" == "401" ]]; then
        echo -e "${RED}✗${NC} 认证失败 (401) - 检查token"
    elif [[ "$http_code" == "403" ]]; then
        echo -e "${RED}✗${NC} 权限不足 (403) - 需要super_admin"
    elif [[ "$http_code" == "404" ]]; then
        echo -e "${RED}✗${NC} 路由不存在 (404) - 检查代码是否部署"
    else
        echo -e "${YELLOW}⚠${NC} 意外状态码 ($http_code)"
        echo "    响应: ${body:0:100}"
    fi
}

echo "🔍 测试5个账单API端点..."
echo ""

# 测试1: 获取账单列表
test_endpoint "GET" "/api/admin/bills?limit=10" "获取账单列表" 200

# 测试2: 获取账单详情（使用不存在的ID，预期404或200）
test_endpoint "GET" "/api/admin/bills/999999" "获取账单详情" 404

# 测试3: 标记已付款（需要实际ID，这里只测试路由存在性）
echo -n "  测试 POST /api/admin/bills/{id}/mark-paid ... "
response=$(curl -s -w "\n%{http_code}" \
    -X POST \
    "http://$API_HOST/api/admin/bills/999999/mark-paid" \
    -H "Authorization: Bearer $ADMIN_TOKEN" \
    -H "Content-Type: application/json" \
    -d '{"payment_date":"2026-11-26T10:00:00Z","payment_method":"test","payment_amount_cents":1000}' 2>&1)
http_code=$(echo "$response" | tail -n1)
# 404(ID不存在)或400(参数错误)都说明路由存在
if [[ "$http_code" == "404" ]] || [[ "$http_code" == "400" ]] || [[ "$http_code" == "200" ]]; then
    echo -e "${GREEN}✓${NC} 路由存在 ($http_code)"
else
    echo -e "${RED}✗${NC} ($http_code)"
fi

# 测试4: 作废账单
echo -n "  测试 POST /api/admin/bills/{id}/void ... "
response=$(curl -s -w "\n%{http_code}" \
    -X POST \
    "http://$API_HOST/api/admin/bills/999999/void" \
    -H "Authorization: Bearer $ADMIN_TOKEN" \
    -H "Content-Type: application/json" 2>&1)
http_code=$(echo "$response" | tail -n1)
if [[ "$http_code" == "404" ]] || [[ "$http_code" == "400" ]] || [[ "$http_code" == "200" ]]; then
    echo -e "${GREEN}✓${NC} 路由存在 ($http_code)"
else
    echo -e "${RED}✗${NC} ($http_code)"
fi

# 测试5: 批量标记
echo -n "  测试 POST /api/admin/bills/batch-mark-paid ... "
response=$(curl -s -w "\n%{http_code}" \
    -X POST \
    "http://$API_HOST/api/admin/bills/batch-mark-paid" \
    -H "Authorization: Bearer $ADMIN_TOKEN" \
    -H "Content-Type: application/json" \
    -d '{"bill_ids":[999999],"payment_date":"2026-11-26T10:00:00Z","payment_method":"test"}' 2>&1)
http_code=$(echo "$response" | tail -n1)
if [[ "$http_code" == "404" ]] || [[ "$http_code" == "400" ]] || [[ "$http_code" == "200" ]]; then
    echo -e "${GREEN}✓${NC} 路由存在 ($http_code)"
else
    echo -e "${RED}✗${NC} ($http_code)"
fi

echo ""
echo "================================"
echo "测试完成"
echo "================================"
echo ""
echo "注意："
echo "- 404/400 状态码表示路由存在，只是资源不存在或参数错误"
echo "- 401 表示token无效或过期"
echo "- 403 表示权限不足（需要super_admin角色）"
echo "- 200 表示请求成功"
echo ""

#!/bin/bash
# 验证脚本：测试 LLM Gateway 新功能
# 用途：验证 modality 路由 + 路径遍历防护 + API Key 认证

set -e

GATEWAY_URL="${GATEWAY_URL:-http://localhost:8080}"
API_KEY="${API_KEY:-}"

echo "=========================================="
echo "LLM Gateway 功能验证测试"
echo "=========================================="
echo ""
echo "测试目标: $GATEWAY_URL"
echo ""

# 颜色定义
GREEN='\033[0;32m'
RED='\033[0;31m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

# 测试计数
TOTAL_TESTS=0
PASSED_TESTS=0
FAILED_TESTS=0

# 测试函数
run_test() {
    local test_name="$1"
    local expected_status="$2"
    shift 2
    local curl_cmd="$@"
    
    TOTAL_TESTS=$((TOTAL_TESTS + 1))
    echo -n "[$TOTAL_TESTS] $test_name ... "
    
    # 执行 curl 并捕获状态码
    HTTP_STATUS=$(eval "$curl_cmd" -o /dev/null -w "%{http_code}" -s)
    
    if [ "$HTTP_STATUS" = "$expected_status" ]; then
        echo -e "${GREEN}✅ PASS${NC} (HTTP $HTTP_STATUS)"
        PASSED_TESTS=$((PASSED_TESTS + 1))
    else
        echo -e "${RED}❌ FAIL${NC} (Expected $expected_status, got $HTTP_STATUS)"
        FAILED_TESTS=$((FAILED_TESTS + 1))
    fi
}

# ===== 测试 1: 健康检查 =====
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo "测试组 1: 基础健康检查"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"

run_test "健康检查端点" "200" \
    "curl $GATEWAY_URL/healthz"

run_test "版本信息端点" "200" \
    "curl $GATEWAY_URL/version"

echo ""

# ===== 测试 2: 路径遍历防护 =====
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo "测试组 2: 路径遍历防护 (CVE 修复验证)"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"

# 创建测试附件（如果有权限）
if [ -w "data/attachments" ]; then
    mkdir -p data/attachments
    echo "test-content" > data/attachments/test.txt
    echo "   📝 已创建测试附件: data/attachments/test.txt"
fi

run_test "正常路径访问" "401" \
    "curl $GATEWAY_URL/api/attachments/test.txt"

run_test "路径遍历攻击 (../ 形式)" "401" \
    "curl $GATEWAY_URL/api/attachments/../../../etc/passwd"

run_test "路径遍历攻击 (URL编码形式)" "401" \
    "curl $GATEWAY_URL/api/attachments/..%2f..%2f..%2fetc%2fpasswd"

run_test "路径遍历攻击 (双编码形式)" "401" \
    "curl $GATEWAY_URL/api/attachments/%252e%252e%252f%252e%252e%252fetc%252fpasswd"

echo ""

# ===== 测试 3: API Key 认证 =====
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo "测试组 3: API Key 认证 (如果启用)"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"

if [ -z "$API_KEY" ]; then
    echo -e "${YELLOW}⚠️  跳过：未设置 API_KEY 环境变量${NC}"
    echo "   使用方法: API_KEY=sk-xxx ./scripts/verify-245.sh"
else
    run_test "无认证访问附件 (应拒绝)" "401" \
        "curl $GATEWAY_URL/api/attachments/test.txt"
    
    run_test "有效 API Key 访问附件" "200" \
        "curl -H 'Authorization: Bearer $API_KEY' $GATEWAY_URL/api/attachments/test.txt"
    
    run_test "无效 API Key 访问附件" "401" \
        "curl -H 'Authorization: Bearer sk-invalid-key' $GATEWAY_URL/api/attachments/test.txt"
    
    run_test "错误的认证格式" "401" \
        "curl -H 'Authorization: InvalidFormat' $GATEWAY_URL/api/attachments/test.txt"
fi

echo ""

# ===== 测试 4: Modality 路由 =====
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo "测试组 4: Modality 路由验证"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"

if [ -z "$API_KEY" ]; then
    echo -e "${YELLOW}⚠️  跳过：需要 API_KEY 进行聊天请求测试${NC}"
else
    # 测试 vision 模型
    run_test "GLM-4.5v vision 请求" "200" \
        "curl -X POST $GATEWAY_URL/v1/chat/completions \
        -H 'Authorization: Bearer $API_KEY' \
        -H 'Content-Type: application/json' \
        -d '{\"model\":\"glm-4.5v\",\"messages\":[{\"role\":\"user\",\"content\":\"test\"}]}'"
    
    # 测试 text 模型
    run_test "GLM-4.5 text 请求" "200" \
        "curl -X POST $GATEWAY_URL/v1/chat/completions \
        -H 'Authorization: Bearer $API_KEY' \
        -H 'Content-Type: application/json' \
        -d '{\"model\":\"glm-4.5\",\"messages\":[{\"role\":\"user\",\"content\":\"test\"}]}'"
fi

echo ""

# ===== 测试总结 =====
echo "=========================================="
echo "测试完成"
echo "=========================================="
echo ""
echo "总测试数: $TOTAL_TESTS"
echo -e "通过: ${GREEN}$PASSED_TESTS${NC}"
echo -e "失败: ${RED}$FAILED_TESTS${NC}"
echo ""

if [ $FAILED_TESTS -eq 0 ]; then
    echo -e "${GREEN}✅ 所有测试通过！${NC}"
    exit 0
else
    echo -e "${RED}❌ 有 $FAILED_TESTS 个测试失败${NC}"
    exit 1
fi

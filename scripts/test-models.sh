#!/bin/bash
# LLM Gateway 综合模型测试脚本
# 用途：验证不同模型和提供商的请求路由功能

set -e

# 配置
GATEWAY_URL="http://localhost:8781"
API_KEY="sk-1vH6C2I9pywyvUXaUXj4vdMZbeYVE5VB0fBYVgqA97JrltE9"
TIMESTAMP=$(date +%s)

# 颜色输出
GREEN='\033[0;32m'
RED='\033[0;31m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

# 测试计数器
TOTAL_TESTS=0
PASSED_TESTS=0
FAILED_TESTS=0

# 日志函数
log_info() {
    echo -e "${YELLOW}[INFO]${NC} $1"
}

log_success() {
    echo -e "${GREEN}[PASS]${NC} $1"
    ((PASSED_TESTS++))
}

log_error() {
    echo -e "${RED}[FAIL]${NC} $1"
    ((FAILED_TESTS++))
}

# 测试单个模型
test_model() {
    local model=$1
    local test_name=$2
    local max_tokens=${3:-10}
    
    ((TOTAL_TESTS++))
    
    log_info "测试 #$TOTAL_TESTS: $test_name (model: $model)"
    
    local request_id="test-${TIMESTAMP}-${TOTAL_TESTS}"
    
    local response=$(timeout 15 curl -s -w "\n%{http_code}" -X POST "$GATEWAY_URL/v1/chat/completions" \
        -H "Authorization: Bearer $API_KEY" \
        -H "Content-Type: application/json" \
        -H "X-Request-Id: $request_id" \
        -d "{
            \"model\": \"$model\",
            \"messages\": [{\"role\": \"user\", \"content\": \"Say: OK\"}],
            \"max_tokens\": $max_tokens
        }" 2>&1)
    
    local http_code=$(echo "$response" | tail -n1)
    local body=$(echo "$response" | head -n-1)
    
    if [ "$http_code" = "200" ]; then
        local has_content=$(echo "$body" | jq -r '.choices[0].message.content' 2>/dev/null)
        if [ "$has_content" != "null" ] && [ -n "$has_content" ]; then
            log_success "$test_name - HTTP $http_code, Content: $has_content"
            echo "         Request ID: $request_id"
            return 0
        else
            log_error "$test_name - HTTP $http_code but no content"
            echo "         Response: $body"
            return 1
        fi
    else
        log_error "$test_name - HTTP $http_code"
        echo "         Response: $body"
        return 1
    fi
}

echo "=========================================="
echo "LLM Gateway 综合模型测试"
echo "=========================================="
echo "Gateway: $GATEWAY_URL"
echo "Timestamp: $TIMESTAMP"
echo ""

# 测试 1: 智谱 GLM-4-Flash
test_model "glm-4-flash" "智谱 GLM-4-Flash" 10

sleep 2

# 测试 2: OpenAI GPT-4o-mini
test_model "gpt-4o-mini" "OpenAI GPT-4o-mini" 10

sleep 2

# 测试 3: DeepSeek V3 (if available)
test_model "deepseek-v3-241226" "DeepSeek V3" 10

sleep 2

# 测试 4: 自动路由（使用 auto 模型）
log_info "测试 #$((TOTAL_TESTS + 1)): 自动路由 (model: auto)"
((TOTAL_TESTS++))

request_id="test-${TIMESTAMP}-auto"
response=$(timeout 15 curl -s -w "\n%{http_code}" -X POST "$GATEWAY_URL/v1/chat/completions" \
    -H "Authorization: Bearer $API_KEY" \
    -H "Content-Type: application/json" \
    -H "X-Request-Id: $request_id" \
    -d '{
        "model": "auto",
        "messages": [{"role": "user", "content": "Say: Hello"}],
        "max_tokens": 10
    }' 2>&1)

http_code=$(echo "$response" | tail -n1)
body=$(echo "$response" | head -n-1)

if [ "$http_code" = "200" ]; then
    chosen_model=$(echo "$body" | jq -r '.model' 2>/dev/null)
    log_success "自动路由 - 选择了模型: $chosen_model"
    echo "         Request ID: $request_id"
    ((PASSED_TESTS++))
else
    log_error "自动路由 - HTTP $http_code"
    echo "         Response: $body"
fi

sleep 2

# 测试 5: 流式响应
log_info "测试 #$((TOTAL_TESTS + 1)): 流式响应 (glm-4-flash)"
((TOTAL_TESTS++))

request_id="test-${TIMESTAMP}-stream"
response=$(timeout 15 curl -s -w "\n%{http_code}" -X POST "$GATEWAY_URL/v1/chat/completions" \
    -H "Authorization: Bearer $API_KEY" \
    -H "Content-Type: application/json" \
    -H "X-Request-Id: $request_id" \
    -d '{
        "model": "glm-4-flash",
        "messages": [{"role": "user", "content": "Say: Test"}],
        "max_tokens": 5,
        "stream": true
    }' 2>&1)

http_code=$(echo "$response" | tail -n1)

if [ "$http_code" = "200" ]; then
    if echo "$response" | grep -q "data:"; then
        log_success "流式响应 - SSE 格式正确"
        echo "         Request ID: $request_id"
        ((PASSED_TESTS++))
    else
        log_error "流式响应 - 未检测到 SSE 数据"
    fi
else
    log_error "流式响应 - HTTP $http_code"
fi

echo ""
echo "=========================================="
echo "测试结果汇总"
echo "=========================================="
echo "总测试数: $TOTAL_TESTS"
echo -e "${GREEN}通过: $PASSED_TESTS${NC}"
echo -e "${RED}失败: $FAILED_TESTS${NC}"
echo "成功率: $(awk "BEGIN {printf \"%.1f\", ($PASSED_TESTS/$TOTAL_TESTS)*100}")%"
echo ""

if [ $FAILED_TESTS -eq 0 ]; then
    echo -e "${GREEN}✅ 所有测试通过！${NC}"
    exit 0
else
    echo -e "${YELLOW}⚠️  有 $FAILED_TESTS 个测试失败${NC}"
    exit 1
fi

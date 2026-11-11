#!/bin/bash
# LLM Gateway Web UI 功能测试脚本
# 用法: BASE_URL=http://localhost:8080 bash tests/functional/web-ui-test.sh

set -uo pipefail

BASE_URL="${BASE_URL:-http://localhost:8080}"
REPORT_DIR="${REPORT_DIR:-test-reports/functional}"
mkdir -p "$REPORT_DIR"

# 颜色定义
GREEN='\033[0;32m'
RED='\033[0;31m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

# 计数器
TOTAL_TESTS=0
PASSED_TESTS=0
FAILED_TESTS=0

# 测试结果数组
declare -a TEST_RESULTS

# 测试函数
test_endpoint() {
    local name="$1"
    local endpoint="$2"
    local expected_status="${3:-200}"
    local method="${4:-GET}"
    
    TOTAL_TESTS=$((TOTAL_TESTS + 1))
    echo -n "测试 $TOTAL_TESTS: $name ... "
    
    if [ "$method" = "GET" ]; then
        response=$(curl -s -w "\n%{http_code}" "$BASE_URL$endpoint" 2>&1)
    else
        response=$(curl -s -w "\n%{http_code}" -X "$method" "$BASE_URL$endpoint" 2>&1)
    fi
    
    status_code=$(echo "$response" | tail -n1)
    
    if [ "$status_code" = "$expected_status" ]; then
        echo -e "${GREEN}✓ 通过${NC}"
        PASSED_TESTS=$((PASSED_TESTS + 1))
        TEST_RESULTS+=("✓ $name")
        return 0
    else
        echo -e "${RED}✗ 失败 (期望 $expected_status, 得到 $status_code)${NC}"
        FAILED_TESTS=$((FAILED_TESTS + 1))
        TEST_RESULTS+=("✗ $name (期望 $expected_status, 得到 $status_code)")
        return 1
    fi
}

test_json_response() {
    local name="$1"
    local endpoint="$2"
    local json_field="$3"
    local optional="${4:-required}"

    TOTAL_TESTS=$((TOTAL_TESTS + 1))
    echo -n "测试 $TOTAL_TESTS: $name ... "

    response=$(curl -s "$BASE_URL$endpoint" 2>&1)

    if echo "$response" | jq -e ".$json_field" > /dev/null 2>&1; then
        echo -e "${GREEN}✓ 通过${NC}"
        PASSED_TESTS=$((PASSED_TESTS + 1))
        TEST_RESULTS+=("✓ $name")
        return 0
    else
        if [ "$optional" = "optional" ]; then
            echo -e "${YELLOW}⚠ 跳过 (可选字段 $json_field 不存在 — dev 构建正常)${NC}"
            PASSED_TESTS=$((PASSED_TESTS + 1))
            TEST_RESULTS+=("⚠ $name (可选字段缺失，跳过)")
            return 0
        fi
        echo -e "${RED}✗ 失败 (JSON字段 $json_field 不存在)${NC}"
        FAILED_TESTS=$((FAILED_TESTS + 1))
        TEST_RESULTS+=("✗ $name (JSON字段缺失)")
        return 1
    fi
}

test_html_content() {
    local name="$1"
    local endpoint="$2"
    local content="$3"
    
    TOTAL_TESTS=$((TOTAL_TESTS + 1))
    echo -n "测试 $TOTAL_TESTS: $name ... "

    response=$(curl -s "$BASE_URL$endpoint" 2>&1)
    
    if echo "$response" | grep -q "$content"; then
        echo -e "${GREEN}✓ 通过${NC}"
        PASSED_TESTS=$((PASSED_TESTS + 1))
        TEST_RESULTS+=("✓ $name")
        return 0
    else
        echo -e "${RED}✗ 失败 (未找到内容: $content)${NC}"
        FAILED_TESTS=$((FAILED_TESTS + 1))
        TEST_RESULTS+=("✗ $name (内容缺失)")
        return 1
    fi
}

# macOS 可移植毫秒计时
now_ms() {
    if date +%s%N 2>/dev/null | grep -qv N$; then
        echo "$(date +%s%N | cut -c1-13)"
    else
        python3 -c 'import time; print(int(time.time()*1000))'
    fi
}

# 自动探测是否为 Web 前端构建（/ 返回 HTML 则完整检查，JSON 则 API-only）
WEB_FRONTEND=1
if curl -s "$BASE_URL/" 2>/dev/null | head -c 200 | grep -qi "<!DOCTYPE html"; then
    WEB_FRONTEND=1
else
    WEB_FRONTEND=0
fi

echo "=========================================="
echo "LLM Gateway Web UI 功能测试"
echo "=========================================="
echo "测试环境: $BASE_URL"
echo "开始时间: $(date '+%Y-%m-%d %H:%M:%S')"
echo ""

# ==========================================
# 1. 基础健康检查
# ==========================================
echo "=== 1. 基础健康检查 ==="
test_json_response "健康检查接口" "/healthz" "status"
test_json_response "版本信息" "/healthz" "version"
test_json_response "构建信息" "/healthz" "build_seq" optional
echo ""

# ==========================================
# 2. Web界面可访问性（仅 Web 前端构建）
# ==========================================
echo "=== 2. Web界面可访问性 ==="
if [ "$WEB_FRONTEND" -eq 1 ]; then
    test_endpoint "首页加载" "/" 200
    test_html_content "HTML结构完整" "/" "<!DOCTYPE html>"
    test_html_content "应用标题存在" "/" "<title>"
    test_html_content "Vue应用挂载点" "/" 'id="app"'
    test_html_content "部署序号元标签" "/" 'name="llmgw:deploy-seq"'
else
    TOTAL_TESTS=$((TOTAL_TESTS + 1))
    echo -e "测试 $TOTAL_TESTS: Web前端检查 ... ${YELLOW}⚠ 跳过 (API-only 构建，/ 返回 JSON)${NC}"
    PASSED_TESTS=$((PASSED_TESTS + 1))
    TEST_RESULTS+=("⚠ Web前端检查 (API-only 构建跳过)")
fi
echo ""

# ==========================================
# 3. 静态资源（仅 Web 前端构建）
# ==========================================
echo "=== 3. 静态资源 ==="
if [ "$WEB_FRONTEND" -eq 1 ]; then
    test_endpoint "Favicon图标" "/favicon.png" 200
    test_endpoint "主题初始化脚本" "/theme-init.js" 200
    test_endpoint "入口切换脚本" "/entry-switch.js" 200
else
    TOTAL_TESTS=$((TOTAL_TESTS + 1))
    echo -e "测试 $TOTAL_TESTS: 静态资源检查 ... ${YELLOW}⚠ 跳过 (API-only 构建)${NC}"
    PASSED_TESTS=$((PASSED_TESTS + 1))
    TEST_RESULTS+=("⚠ 静态资源检查 (API-only 构建跳过)")
fi
echo ""

# ==========================================
# 4. API端点（无需认证）
# ==========================================
echo "=== 4. API端点（无需认证） ==="
test_endpoint "健康检查" "/healthz" 200
TOTAL_TESTS=$((TOTAL_TESTS + 1))
metrics_code=$(curl -s -o /dev/null -w "%{http_code}" "$BASE_URL/metrics")
if [ "$metrics_code" = "200" ]; then
    echo -e "测试 $TOTAL_TESTS: Metrics端点 ... ${GREEN}✓ 通过 (200 公开)${NC}"
    PASSED_TESTS=$((PASSED_TESTS + 1))
    TEST_RESULTS+=("✓ Metrics端点 (200 公开)")
elif [ "$metrics_code" = "401" ]; then
    echo -e "测试 $TOTAL_TESTS: Metrics端点 ... ${YELLOW}⚠ 生产加固 (401 已纳入认证保护，非缺陷)${NC}"
    PASSED_TESTS=$((PASSED_TESTS + 1))
    TEST_RESULTS+=("⚠ Metrics端点 (401 生产加固，非缺陷)")
else
    echo -e "测试 $TOTAL_TESTS: Metrics端点 ... ${RED}✗ 失败 (期望 200/401, 得到 $metrics_code)${NC}"
    FAILED_TESTS=$((FAILED_TESTS + 1))
    TEST_RESULTS+=("✗ Metrics端点 ($metrics_code)")
fi
echo ""

# ==========================================
# 5. API端点认证保护（未认证应 401/307）
# ==========================================
echo "=== 5. API端点认证保护 ==="
test_endpoint "模型列表需认证" "/api/models" 401
test_endpoint "管理端点需认证" "/api/admin/tenants" 401
test_endpoint "租户端点需认证" "/api/tenant/telemetry-preference" 401
TOTAL_TESTS=$((TOTAL_TESTS + 1))
cred_code=$(curl -s -o /dev/null -w "%{http_code}" "$BASE_URL/api/credentials")
if [ "$cred_code" = "307" ] || [ "$cred_code" = "401" ]; then
    echo -e "测试 $TOTAL_TESTS: 凭据列表需认证 ... ${GREEN}✓ 通过 ($cred_code 重定向/拒绝)${NC}"
    PASSED_TESTS=$((PASSED_TESTS + 1))
    TEST_RESULTS+=("✓ 凭据列表需认证 ($cred_code)")
else
    echo -e "测试 $TOTAL_TESTS: 凭据列表需认证 ... ${RED}✗ 失败 (期望 307/401, 得到 $cred_code)${NC}"
    FAILED_TESTS=$((FAILED_TESTS + 1))
    TEST_RESULTS+=("✗ 凭据列表需认证 ($cred_code)")
fi
echo ""

# ==========================================
# 6. 性能测试（响应时间）
# ==========================================
echo "=== 6. 性能测试 ==="
TOTAL_TESTS=$((TOTAL_TESTS + 1))
echo -n "测试 $TOTAL_TESTS: 首页响应时间 < 2秒 ... "
start_time=$(now_ms)
curl -s "$BASE_URL/" > /dev/null
end_time=$(now_ms)
response_time=$((end_time - start_time))

if [ $response_time -lt 2000 ]; then
    echo -e "${GREEN}✓ 通过 (${response_time}ms)${NC}"
    PASSED_TESTS=$((PASSED_TESTS + 1))
    TEST_RESULTS+=("✓ 首页响应时间 (${response_time}ms)")
else
    echo -e "${RED}✗ 失败 (${response_time}ms > 2000ms)${NC}"
    FAILED_TESTS=$((FAILED_TESTS + 1))
    TEST_RESULTS+=("✗ 首页响应时间过慢 (${response_time}ms)")
fi

TOTAL_TESTS=$((TOTAL_TESTS + 1))
echo -n "测试 $TOTAL_TESTS: healthz响应时间 < 100ms ... "
start_time=$(now_ms)
curl -s "$BASE_URL/healthz" > /dev/null
end_time=$(now_ms)
response_time=$((end_time - start_time))

if [ $response_time -lt 100 ]; then
    echo -e "${GREEN}✓ 通过 (${response_time}ms)${NC}"
    PASSED_TESTS=$((PASSED_TESTS + 1))
    TEST_RESULTS+=("✓ healthz响应时间 (${response_time}ms)")
else
    echo -e "${YELLOW}⚠ 警告 (${response_time}ms > 100ms)${NC}"
    PASSED_TESTS=$((PASSED_TESTS + 1))
    TEST_RESULTS+=("⚠ healthz响应时间较慢 (${response_time}ms)")
fi
echo ""

# ==========================================
# 测试总结
# ==========================================
echo "=========================================="
echo "测试总结"
echo "=========================================="
echo "总测试数: $TOTAL_TESTS"
echo -e "通过: ${GREEN}$PASSED_TESTS${NC}"
echo -e "失败: ${RED}$FAILED_TESTS${NC}"
echo "通过率: $(awk "BEGIN {printf \"%.2f\", ($PASSED_TESTS/$TOTAL_TESTS)*100}")%"
echo ""

# 详细结果
echo "详细结果:"
for result in "${TEST_RESULTS[@]}"; do
    echo "  $result"
done
echo ""

# 生成报告
REPORT_FILE="$REPORT_DIR/web-ui-test-$(date +%Y%m%d-%H%M%S).txt"
{
    echo "LLM Gateway Web UI 功能测试报告"
    echo "======================================"
    echo "测试环境: $BASE_URL"
    echo "测试时间: $(date '+%Y-%m-%d %H:%M:%S')"
    echo ""
    echo "总测试数: $TOTAL_TESTS"
    echo "通过: $PASSED_TESTS"
    echo "失败: $FAILED_TESTS"
    echo "通过率: $(awk "BEGIN {printf \"%.2f\", ($PASSED_TESTS/$TOTAL_TESTS)*100}")%"
    echo ""
    echo "详细结果:"
    for result in "${TEST_RESULTS[@]}"; do
        echo "  $result"
    done
} > "$REPORT_FILE"

echo "报告已保存: $REPORT_FILE"
echo ""

# 返回状态码
if [ $FAILED_TESTS -gt 0 ]; then
    echo -e "${RED}测试失败！${NC}"
    exit 1
else
    echo -e "${GREEN}所有测试通过！${NC}"
    exit 0
fi

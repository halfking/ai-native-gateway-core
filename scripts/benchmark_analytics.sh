#!/bin/bash
# 会话分析端点性能压测脚本
# 使用 Apache Bench (ab) 进行性能测试

set -e

# 配置
BASE_URL="${BASE_URL:-http://localhost:8080}"
TOKEN="${AUTH_TOKEN:-}"
RESULTS_DIR="./benchmark_results"

# 颜色输出
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

echo "=========================================="
echo "会话分析端点性能压测"
echo "=========================================="
echo "目标服务: $BASE_URL"
echo "结果目录: $RESULTS_DIR"
echo ""

# 创建结果目录
mkdir -p "$RESULTS_DIR"

# 检查 ab 工具
if ! command -v ab &> /dev/null; then
    echo -e "${RED}错误: Apache Bench (ab) 未安装${NC}"
    echo "macOS 安装: brew install httpd"
    echo "Linux 安装: apt-get install apache2-utils"
    exit 1
fi

# 检查服务是否运行
if ! curl -s "$BASE_URL/health" > /dev/null 2>&1; then
    echo -e "${RED}错误: 服务未运行 ($BASE_URL)${NC}"
    exit 1
fi

echo -e "${GREEN}✓ 服务运行正常${NC}"
echo ""

# 构建 Header
AUTH_HEADER=""
if [ -n "$TOKEN" ]; then
    AUTH_HEADER="-H \"Authorization: Bearer $TOKEN\""
fi

# 测试函数
run_test() {
    local name="$1"
    local url="$2"
    local concurrency="${3:-10}"
    local requests="${4:-100}"
    local target_p90="${5:-1000}"
    
    echo "----------------------------------------"
    echo "测试: $name"
    echo "URL: $url"
    echo "并发: $concurrency, 请求数: $requests"
    echo "目标 P90: ${target_p90}ms"
    echo "----------------------------------------"
    
    local output_file="$RESULTS_DIR/${name// /_}.txt"
    
    # 执行压测
    ab -n "$requests" -c "$concurrency" \
       -g "$RESULTS_DIR/${name// /_}.tsv" \
       $AUTH_HEADER \
       "$url" > "$output_file" 2>&1
    
    # 解析结果
    local p50=$(grep "50%" "$output_file" | awk '{print $2}')
    local p90=$(grep "90%" "$output_file" | awk '{print $2}')
    local p99=$(grep "99%" "$output_file" | awk '{print $2}')
    local rps=$(grep "Requests per second" "$output_file" | awk '{print $4}')
    
    echo "结果:"
    echo "  RPS: $rps"
    echo "  P50: ${p50}ms"
    echo "  P90: ${p90}ms"
    echo "  P99: ${p99}ms"
    
    # 判断是否达标
    if [ "${p90%%.*}" -le "$target_p90" ]; then
        echo -e "  状态: ${GREEN}✓ 达标${NC}"
    else
        echo -e "  状态: ${RED}✗ 未达标${NC} (P90 ${p90}ms > ${target_p90}ms)"
    fi
    
    echo ""
}

# 测试套件
echo "开始压测..."
echo ""

# 1. KPI Stats (P90 < 500ms)
run_test "KPI Stats" \
    "$BASE_URL/api/admin/session-analytics/stats" \
    10 200 500

# 2. 活动趋势 7天 (P90 < 1500ms)
DATE_FROM=$(date -v-7d +%Y-%m-%d)
DATE_TO=$(date +%Y-%m-%d)
run_test "Activity 7 Days" \
    "$BASE_URL/api/admin/session-analytics/activity?date_from=$DATE_FROM&date_to=$DATE_TO" \
    5 100 1500

# 3. 活动趋势 90天 (P90 < 5000ms)
DATE_FROM_90=$(date -v-90d +%Y-%m-%d)
run_test "Activity 90 Days" \
    "$BASE_URL/api/admin/session-analytics/activity?date_from=$DATE_FROM_90&date_to=$DATE_TO" \
    5 50 5000

# 4. 模型分布 (P90 < 2000ms)
run_test "Model Breakdown" \
    "$BASE_URL/api/admin/session-analytics/model-breakdown?date_from=$DATE_FROM&date_to=$DATE_TO" \
    5 100 2000

# 5. 会话列表 (P90 < 1000ms)
run_test "Session List" \
    "$BASE_URL/api/admin/session-analytics?page=1&page_size=20" \
    10 200 1000

echo "=========================================="
echo "压测完成"
echo "=========================================="
echo "详细结果已保存到: $RESULTS_DIR"
echo ""
echo "生成报告:"
echo "  cat $RESULTS_DIR/*.txt | grep -E '(测试:|P90:|状态:)'"

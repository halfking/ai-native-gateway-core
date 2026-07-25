#!/bin/bash
# LLM Gateway 优化部署验证脚本
# 用途：验证所有新功能和优化是否正常工作

set -e  # 遇到错误立即退出

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(dirname "$SCRIPT_DIR")"
cd "$PROJECT_ROOT"

echo "=========================================="
echo "LLM Gateway 优化部署验证"
echo "=========================================="
echo ""

# 颜色定义
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

# 测试结果统计
TOTAL_TESTS=0
PASSED_TESTS=0
FAILED_TESTS=0

# 运行测试并记录结果
run_test() {
    local test_name="$1"
    local test_cmd="$2"
    
    TOTAL_TESTS=$((TOTAL_TESTS + 1))
    echo -n "[$TOTAL_TESTS] 测试: $test_name ... "
    
    if eval "$test_cmd" > /tmp/test_output_$TOTAL_TESTS.log 2>&1; then
        echo -e "${GREEN}✓ PASS${NC}"
        PASSED_TESTS=$((PASSED_TESTS + 1))
        return 0
    else
        echo -e "${RED}✗ FAIL${NC}"
        FAILED_TESTS=$((FAILED_TESTS + 1))
        echo "  错误详情: $(tail -3 /tmp/test_output_$TOTAL_TESTS.log)"
        return 1
    fi
}

echo "阶段 1: 编译检查"
echo "----------------------------------------"

run_test "Go 模块整理" "go mod tidy"
run_test "代码编译检查" "go build -o /tmp/llm-gateway-test-build ./cmd/gateway"

echo ""
echo "阶段 2: 单元测试（无竞争检测）"
echo "----------------------------------------"

run_test "原始日志记录器测试" "go test ./internal/logging -run TestRawDataLogger -timeout 10s"
run_test "语义分析器测试" "go test ./internal/ir -run TestSemanticAnalyzer -timeout 10s"
run_test "AtomicSession 基础测试" "go test ./domains/session -run TestAtomicSession -timeout 30s"
run_test "无锁队列测试" "go test ./internal/logging -run TestLockFreeQueue -timeout 60s"

echo ""
echo "阶段 3: 并发测试（Race Detector）"
echo "----------------------------------------"

run_test "AtomicSession 并发竞争检测" "go test -race ./domains/session -run TestAtomicSessionConcurrentUpdates -timeout 30s"
run_test "无锁队列并发竞争检测" "go test -race ./internal/logging -run TestLockFreeQueueConcurrent -timeout 90s"
run_test "混合读写竞争检测" "go test -race ./domains/session -run TestAtomicSessionReadWriteConcurrent -timeout 30s"

echo ""
echo "阶段 4: 性能基准测试"
echo "----------------------------------------"

run_test "AtomicSession 性能基准" "go test ./domains/session -bench=BenchmarkAtomicSession -benchtime=1s -run=^$ -timeout 30s"
run_test "无锁队列性能基准" "go test ./internal/logging -bench=BenchmarkLockFreeQueue -benchtime=1s -run=^$ -timeout 30s"

echo ""
echo "阶段 5: 集成验证"
echo "----------------------------------------"

# 创建临时测试目录
TEST_DIR="/tmp/llm-gateway-test-$$"
mkdir -p "$TEST_DIR/logs/raw_data"

run_test "异步日志记录器初始化" "go test ./internal/logging -run TestAsyncRawDataLogger -timeout 10s"

# 清理
rm -rf "$TEST_DIR"
rm -f /tmp/test_output_*.log

echo ""
echo "=========================================="
echo "验证结果汇总"
echo "=========================================="
echo -e "总计: $TOTAL_TESTS 项测试"
echo -e "${GREEN}通过: $PASSED_TESTS 项${NC}"
echo -e "${RED}失败: $FAILED_TESTS 项${NC}"
echo ""

if [ $FAILED_TESTS -eq 0 ]; then
    echo -e "${GREEN}✓ 所有测试通过！可以部署。${NC}"
    echo ""
    echo "建议的部署步骤："
    echo "1. 配置环境变量（见 FINAL_IMPLEMENTATION_REPORT.md）"
    echo "2. 修改 cmd/gateway/main.go 中的初始化代码"
    echo "3. 编译: make build"
    echo "4. 灰度发布: 10% 流量观察 24 小时"
    echo "5. 监控关键指标（见文档）"
    echo "6. 逐步扩大到 100% 流量"
    exit 0
else
    echo -e "${RED}✗ 存在失败的测试，请修复后再部署。${NC}"
    echo ""
    echo "调试建议："
    echo "1. 查看详细日志: cat /tmp/test_output_*.log"
    echo "2. 单独运行失败的测试: go test -v ./... -run <测试名>"
    echo "3. 启用详细日志: go test -v -race ..."
    exit 1
fi

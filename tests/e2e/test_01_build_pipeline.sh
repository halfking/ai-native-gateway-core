#!/usr/bin/env bash
# E2E Test 01: 构建流水线测试

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
TEST_LIB="$PROJECT_ROOT/tests/lib/common.sh"

source "$TEST_LIB"

print_test_header "E2E Test 01: 构建流水线验证"

# 测试 1.1: 环境验证
run_test_case "环境验证" "bash $PROJECT_ROOT/scripts/build/test-build-dry-run.sh"

# 测试 1.2: 后端编译（小版本）
TEST_VERSION="2.4.8-e2e-$(date +%s)"
print_info "使用测试版本: $TEST_VERSION"

# 创建临时构建目录
TEST_BUILD_DIR="/tmp/e2e-build-$$"
mkdir -p "$TEST_BUILD_DIR/bin"

run_test_case "后端编译-linux-amd64" \
    "cd $PROJECT_ROOT && CGO_ENABLED=1 GOOS=linux GOARCH=amd64 go build -ldflags '-X main.Version=$TEST_VERSION' -o $TEST_BUILD_DIR/bin/test-binary ./cmd/gateway 2>&1 | head -10"

# 验证二进制存在
if [ -f "$TEST_BUILD_DIR/bin/test-binary" ]; then
    print_success "二进制文件已生成: $(ls -lh $TEST_BUILD_DIR/bin/test-binary | awk '{print $5}')"
    record_test_result "二进制文件验证" "PASS" "0"
    TESTS_PASSED=$((TESTS_PASSED - 1 + 1))
    TESTS_TOTAL=$((TESTS_TOTAL - 1))
else
    print_error "二进制文件未生成"
    record_test_result "二进制文件验证" "FAIL" "0"
    TESTS_FAILED=$((TESTS_FAILED - 1 + 1))
    TESTS_TOTAL=$((TESTS_TOTAL - 1))
fi

# 测试 1.3: 跨平台编译验证（不实际编译）
run_test_case "Go环境检查" "go version"

# 测试 1.4: 验证脚本可执行
run_test_case "构建脚本可执行" "[ -x $PROJECT_ROOT/scripts/build/build-pipeline.sh ]"

# 清理
rm -rf "$TEST_BUILD_DIR"

print_test_summary
exit $?


#!/usr/bin/env bash
# ★★ r1048 轮（★ 2026-10-29）：★ 本脚本在【宿主】上编译 Go ⇒ ⇒ 子进程会往 Go 的
# ★★ 构建缓存里写产物 ⇒ ⇒ 而那个缓存默认是【用户那个全局的】⇒ ⇒ 必须指向【专用缓存】
# ★★ （★ 目录约定与 6 GB 上限见 `scripts/lib/gocache.sh`；★ 理由与实测读数见
# ★★ `internal/gocache` 的包注释 ⇒ ⇒ ★★ 实测：★ **单包** `go test ./admin/` 就要
# ★★ **846 MB** ⇒ ⇒ ★★ 体积由【传递依赖闭包】决定 ⇒ ⇒ ★★ 按「编译范围」分层不成立）。
# ★★ 【失败安全】：★ 找不到共享入口 ⇒ ⇒ **不设 GOCACHE**（★ 宁可漏隔离 ⇒ ⇒ 也不要把
# ★★ GOCACHE 设成垃圾值 ⇒ ⇒ 那会让 go 直接报错 ⇒ ⇒ 把脚本弄坏）。
__gocache_dir="$(cd "$(dirname "${BASH_SOURCE[0]:-$0}")" 2>/dev/null && pwd)" || __gocache_dir=""
while [ -n "$__gocache_dir" ] && [ ! -f "$__gocache_dir/scripts/lib/gocache.sh" ] && [ "$__gocache_dir" != "/" ]; do
  __gocache_dir="$(dirname "$__gocache_dir")"
done
if [ -n "$__gocache_dir" ] && [ -f "$__gocache_dir/scripts/lib/gocache.sh" ]; then
  . "$__gocache_dir/scripts/lib/gocache.sh"
  gocache_use script
fi
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


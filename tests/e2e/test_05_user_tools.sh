#!/usr/bin/env bash
# E2E Test 05: 用户工具验证

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
TEST_LIB="$PROJECT_ROOT/tests/lib/common.sh"

source "$TEST_LIB"

print_test_header "E2E Test 05: 用户工具验证"

USER_TOOLS=(
    "scripts/install/install.sh"
    "scripts/install/upgrade.sh"
    "scripts/install/uninstall.sh"
    "scripts/install/backup.sh"
    "scripts/install/activate.sh"
)

# 测试 1: 所有用户工具存在且可执行
print_step "检查用户工具"
ALL_EXIST=true
for tool in "${USER_TOOLS[@]}"; do
    full_path="$PROJECT_ROOT/$tool"
    if [ -x "$full_path" ]; then
        print_info "✅ $tool"
    else
        print_info "❌ $tool"
        ALL_EXIST=false
    fi
done

if [ "$ALL_EXIST" = true ]; then
    record_test_result "用户工具存在且可执行" "PASS" "0"
else
    record_test_result "用户工具存在且可执行" "FAIL" "0"
fi

# 测试 2: upgrade.sh 帮助
print_step "测试 upgrade.sh 帮助"
if bash "$PROJECT_ROOT/scripts/install/upgrade.sh" --help > /tmp/upgrade_help.log 2>&1; then
    if grep -q "用法" /tmp/upgrade_help.log; then
        record_test_result "upgrade.sh --help" "PASS" "0"
    else
        record_test_result "upgrade.sh --help" "FAIL" "0"
    fi
else
    record_test_result "upgrade.sh --help" "FAIL" "0"
fi

# 测试 3: uninstall.sh 帮助
print_step "测试 uninstall.sh 帮助"
if bash "$PROJECT_ROOT/scripts/install/uninstall.sh" --help > /tmp/uninstall_help.log 2>&1; then
    if grep -q "用法" /tmp/uninstall_help.log; then
        record_test_result "uninstall.sh --help" "PASS" "0"
    else
        record_test_result "uninstall.sh --help" "FAIL" "0"
    fi
else
    record_test_result "uninstall.sh --help" "FAIL" "0"
fi

# 测试 4: backup.sh 语法
print_step "测试 backup.sh 语法"
if bash -n "$PROJECT_ROOT/scripts/install/backup.sh"; then
    record_test_result "backup.sh 语法" "PASS" "0"
else
    record_test_result "backup.sh 语法" "FAIL" "0"
fi

# 测试 5: activate.sh 语法
print_step "测试 activate.sh 语法"
if bash -n "$PROJECT_ROOT/scripts/install/activate.sh"; then
    record_test_result "activate.sh 语法" "PASS" "0"
else
    record_test_result "activate.sh 语法" "FAIL" "0"
fi

# 测试 6: 错误处理
print_step "测试错误处理（无root权限）"
INSTALL_OUTPUT=$(bash "$PROJECT_ROOT/scripts/install/install.sh" 2>&1 || true)
if echo "$INSTALL_OUTPUT" | grep -q "root 权限"; then
    record_test_result "权限检查正常" "PASS" "0"
else
    record_test_result "权限检查正常" "FAIL" "0"
fi

print_test_summary
exit $?


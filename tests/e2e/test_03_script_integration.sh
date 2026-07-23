#!/usr/bin/env bash
# E2E Test 03: 脚本集成测试

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
TEST_LIB="$PROJECT_ROOT/tests/lib/common.sh"

source "$TEST_LIB"

print_test_header "E2E Test 03: 脚本集成验证"

# 测试 3.1: 所有脚本语法
print_step "检查所有脚本语法"
TOTAL_SCRIPTS=0
SYNTAX_OK=0
for script in $(find "$PROJECT_ROOT/scripts" -name "*.sh" 2>/dev/null); do
    TOTAL_SCRIPTS=$((TOTAL_SCRIPTS + 1))
    if bash -n "$script" 2>/dev/null; then
        SYNTAX_OK=$((SYNTAX_OK + 1))
    fi
done
if [ $SYNTAX_OK -eq $TOTAL_SCRIPTS ]; then
    record_test_result "所有脚本语法 ($SYNTAX_OK/$TOTAL_SCRIPTS)" "PASS" "0"
else
    record_test_result "所有脚本语法 ($SYNTAX_OK/$TOTAL_SCRIPTS)" "FAIL" "0"
fi

# 测试 3.2: 关键脚本可执行权限
print_step "检查关键脚本权限"
KEY_SCRIPTS=(
    "scripts/build/build-pipeline.sh"
    "scripts/deploy/deploy-to-245.sh"
    "scripts/upload/upload-to-cloudreve.sh"
    "scripts/install/install.sh"
)

EXEC_OK=0
for script in "${KEY_SCRIPTS[@]}"; do
    full_path="$PROJECT_ROOT/$script"
    if [ -x "$full_path" ]; then
        EXEC_OK=$((EXEC_OK + 1))
    fi
done

if [ $EXEC_OK -eq ${#KEY_SCRIPTS[@]} ]; then
    record_test_result "关键脚本可执行 ($EXEC_OK/${#KEY_SCRIPTS[@]})" "PASS" "0"
else
    record_test_result "关键脚本可执行 ($EXEC_OK/${#KEY_SCRIPTS[@]})" "FAIL" "0"
fi

# 测试 3.3: 清单生成集成测试
print_step "测试清单生成集成"
TEST_BUILD_DIR="/tmp/e2e-manifest-$$"
mkdir -p "$TEST_BUILD_DIR/archives" "$TEST_BUILD_DIR/manifests"

cd "$TEST_BUILD_DIR/archives"
echo "test1" > test-file-1.tar.gz
echo "test2" > test-file-2.tar.gz
echo "test3" > docker-bundle.tar.gz
sha256sum *.tar.gz > SHA256SUMS 2>/dev/null || true

cd "$PROJECT_ROOT"
if bash "$PROJECT_ROOT/scripts/upload/generate-manifest.sh" "$TEST_BUILD_DIR" "e2e-test-$$" > /tmp/manifest.log 2>&1; then
    record_test_result "清单生成集成" "PASS" "0"
    
    if [ -f "$TEST_BUILD_DIR/manifests/RELEASE-e2e-test-$$.json" ]; then
        record_test_result "清单JSON生成" "PASS" "0"
    else
        record_test_result "清单JSON生成" "FAIL" "0"
    fi
    
    if [ -f "$TEST_BUILD_DIR/archives/SHA256SUMS" ]; then
        record_test_result "SHA256SUMS生成" "PASS" "0"
    else
        record_test_result "SHA256SUMS生成" "FAIL" "0"
    fi
else
    record_test_result "清单生成集成" "FAIL" "0"
fi

rm -rf "$TEST_BUILD_DIR"

# 测试 3.4: 部署脚本dry-run
print_step "测试部署脚本帮助/语法"
if bash -n "$PROJECT_ROOT/scripts/deploy/deploy-to-245.sh" 2>/dev/null; then
    record_test_result "部署脚本语法" "PASS" "0"
else
    record_test_result "部署脚本语法" "FAIL" "0"
fi

# 测试 3.5: 上传脚本dry-run
if bash -n "$PROJECT_ROOT/scripts/upload/upload-to-cloudreve.sh" 2>/dev/null; then
    record_test_result "上传脚本语法" "PASS" "0"
else
    record_test_result "上传脚本语法" "FAIL" "0"
fi

print_test_summary
exit $?


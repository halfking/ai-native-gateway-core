#!/usr/bin/env bash
# E2E 测试主运行器 - 执行所有测试

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"

echo "========================================="
echo "E2E 测试套件 - 网关打包与下载自动化"
echo "========================================="
echo ""
echo "测试时间: $(date +'%Y-%m-%d %H:%M:%S')"
echo "项目目录: $PROJECT_ROOT"
echo ""

# 测试列表
TESTS=(
    "test_01_build_pipeline.sh"
    "test_02_database_operations.sh"
    "test_03_script_integration.sh"
    "test_04_complete_workflow.sh"
)

TOTAL_TESTS=0
TOTAL_PASSED=0
TOTAL_FAILED=0
TOTAL_SKIPPED=0

for test_script in "${TESTS[@]}"; do
    test_path="$SCRIPT_DIR/$test_script"
    
    if [ ! -f "$test_path" ]; then
        echo "❌ 测试文件不存在: $test_script"
        continue
    fi
    
    echo ""
    echo "==========================================="
    echo "执行: $test_script"
    echo "==========================================="
    
    if bash "$test_path"; then
        echo "✅ $test_script 通过"
    else
        echo "❌ $test_script 失败"
    fi
done

echo ""
echo "========================================="
echo "E2E 测试套件完成"
echo "========================================="


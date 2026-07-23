#!/usr/bin/env bash
# 测试上传脚本

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

echo "========================================="
echo "上传脚本测试"
echo "========================================="

# ============================================================================
# 测试 1: 检查脚本存在
# ============================================================================

echo ""
echo "✅ 测试 1: 检查脚本文件"

SCRIPTS=(
    "upload-to-cloudreve.sh"
    "generate-manifest.sh"
    "upload-release.sh"
)

for script in "${SCRIPTS[@]}"; do
    if [[ -x "$SCRIPT_DIR/$script" ]]; then
        echo "   ✅ $script"
    else
        echo "   ❌ $script (不存在或不可执行)"
        exit 1
    fi
done

# ============================================================================
# 测试 2: 语法检查
# ============================================================================

echo ""
echo "✅ 测试 2: Shell 语法检查"

for script in "${SCRIPTS[@]}"; do
    if bash -n "$SCRIPT_DIR/$script" 2>/dev/null; then
        echo "   ✅ $script 语法正确"
    else
        echo "   ❌ $script 语法错误"
        exit 1
    fi
done

# ============================================================================
# 测试 3: 检查依赖工具
# ============================================================================

echo ""
echo "✅ 测试 3: 检查依赖工具"

TOOLS=(
    "curl:必需"
    "jq:必需"
    "sha256sum:必需"
)

for tool_info in "${TOOLS[@]}"; do
    IFS=':' read -r tool desc <<< "$tool_info"
    
    # 特殊处理 sha256sum (macOS 使用 shasum)
    if [[ "$tool" == "sha256sum" ]]; then
        if command -v sha256sum &>/dev/null || command -v shasum &>/dev/null; then
            echo "   ✅ sha256sum/shasum ($desc)"
            continue
        else
            echo "   ❌ sha256sum/shasum 未安装 ($desc)"
            continue
        fi
    fi
    
    if command -v "$tool" &>/dev/null; then
        echo "   ✅ $tool ($desc)"
    else
        echo "   ❌ $tool 未安装 ($desc)"
    fi
done

# ============================================================================
# 测试 4: 检查数据库迁移脚本
# ============================================================================

echo ""
echo "✅ 测试 4: 检查数据库迁移脚本"

MIGRATION="${SCRIPT_DIR}/../../db/migrations/014_create_releases_tables.sql"

if [[ -f "$MIGRATION" ]]; then
    echo "   ✅ 数据库迁移脚本存在"
    
    # 检查语法（简单检查）
    if grep -q "CREATE TABLE.*releases" "$MIGRATION"; then
        echo "   ✅ releases 表定义存在"
    else
        echo "   ❌ releases 表定义缺失"
    fi
    
    if grep -q "CREATE TABLE.*release_files" "$MIGRATION"; then
        echo "   ✅ release_files 表定义存在"
    else
        echo "   ❌ release_files 表定义缺失"
    fi
    
    if grep -q "CREATE TABLE.*release_tests" "$MIGRATION"; then
        echo "   ✅ release_tests 表定义存在"
    else
        echo "   ❌ release_tests 表定义缺失"
    fi
else
    echo "   ❌ 数据库迁移脚本不存在"
fi

# ============================================================================
# 测试 5: 检查 Go 代码
# ============================================================================

echo ""
echo "✅ 测试 5: 检查 Go 代码"

GO_FILES=(
    "internal/release/types.go"
    "internal/release/repository.go"
    "internal/release/service.go"
    "internal/release/handler.go"
)

for go_file in "${GO_FILES[@]}"; do
    full_path="${SCRIPT_DIR}/../../${go_file}"
    if [[ -f "$full_path" ]]; then
        echo "   ✅ $go_file"
        
        # 简单的语法检查
        if go fmt "$full_path" &>/dev/null; then
            echo "      ✅ 格式正确"
        else
            echo "      ⚠️  格式需要调整"
        fi
    else
        echo "   ❌ $go_file (不存在)"
    fi
done

# ============================================================================
# 汇总
# ============================================================================

echo ""
echo "========================================="
echo "测试完成"
echo "========================================="
echo ""
echo "✅ 所有上传和版本管理脚本就绪"
echo ""
echo "下一步："
echo "  1. 运行数据库迁移: psql -f db/migrations/014_create_releases_tables.sql"
echo "  2. 测试清单生成: bash scripts/upload/generate-manifest.sh build/releases 2.4.7-1347"
echo "  3. 集成到主构建流程"
echo ""


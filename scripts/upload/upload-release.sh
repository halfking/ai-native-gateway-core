#!/usr/bin/env bash
# scripts/upload/upload-release.sh - 批量上传发布文件
# 用法：bash upload-release.sh <build_output_dir> <version>

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# ============================================================================
# 配置
# ============================================================================

BUILD_OUTPUT="${1:?Usage: $0 <build_output_dir> <version>}"
VERSION="${2:?Usage: $0 <build_output_dir> <version>}"

ARCHIVES_DIR="${BUILD_OUTPUT}/archives"

if [[ ! -d "$ARCHIVES_DIR" ]]; then
    echo "❌ 目录不存在: $ARCHIVES_DIR"
    exit 1
fi

echo "========================================="
echo "批量上传发布文件"
echo "========================================="
echo ""
echo "版本: $VERSION"
echo "构建输出: $BUILD_OUTPUT"
echo ""

# ============================================================================
# 步骤 1: 生成清单
# ============================================================================

echo "📋 步骤 1: 生成版本清单"
bash "${SCRIPT_DIR}/generate-manifest.sh" "$BUILD_OUTPUT" "$VERSION"

if [[ $? -ne 0 ]]; then
    echo "❌ 清单生成失败"
    exit 1
fi

# ============================================================================
# 步骤 2: 上传所有文件
# ============================================================================

echo ""
echo "📤 步骤 2: 上传所有文件"
echo ""

UPLOADED_FILES=()
FAILED_FILES=()

for archive in "${ARCHIVES_DIR}"/*.tar.gz "${ARCHIVES_DIR}"/*.zip; do
    if [[ ! -f "$archive" ]]; then
        continue
    fi
    
    filename=$(basename "$archive")
    echo "上传: $filename"
    
    if bash "${SCRIPT_DIR}/upload-to-cloudreve.sh" "$archive" "$VERSION"; then
        UPLOADED_FILES+=("$filename")
        echo "   ✅ 成功"
    else
        FAILED_FILES+=("$filename")
        echo "   ❌ 失败"
    fi
    
    echo ""
done

# ============================================================================
# 步骤 3: 上传清单文件
# ============================================================================

echo ""
echo "📄 步骤 3: 上传清单文件"

MANIFEST_JSON="${BUILD_OUTPUT}/manifests/RELEASE-${VERSION}.json"
MANIFEST_MD="${BUILD_OUTPUT}/manifests/RELEASE-${VERSION}.md"

if [[ -f "$MANIFEST_JSON" ]]; then
    bash "${SCRIPT_DIR}/upload-to-cloudreve.sh" "$MANIFEST_JSON" "$VERSION"
fi

if [[ -f "$MANIFEST_MD" ]]; then
    bash "${SCRIPT_DIR}/upload-to-cloudreve.sh" "$MANIFEST_MD" "$VERSION"
fi

# ============================================================================
# 步骤 4: 上传 SHA256SUMS
# ============================================================================

echo ""
echo "🔐 步骤 4: 上传校验和"

SHA256SUMS="${ARCHIVES_DIR}/SHA256SUMS"
if [[ -f "$SHA256SUMS" ]]; then
    bash "${SCRIPT_DIR}/upload-to-cloudreve.sh" "$SHA256SUMS" "$VERSION"
fi

# ============================================================================
# 汇总
# ============================================================================

echo ""
echo "========================================="
echo "上传完成"
echo "========================================="
echo ""
echo "成功: ${#UPLOADED_FILES[@]} 个文件"
for file in "${UPLOADED_FILES[@]}"; do
    echo "  ✅ $file"
done

if [[ ${#FAILED_FILES[@]} -gt 0 ]]; then
    echo ""
    echo "失败: ${#FAILED_FILES[@]} 个文件"
    for file in "${FAILED_FILES[@]}"; do
        echo "  ❌ $file"
    done
    exit 1
fi

echo ""
echo "🎉 所有文件已上传到 Cloudreve"
echo ""
echo "下载地址:"
echo "  https://files.kxpms.cn/llm-gateway-go/releases/${VERSION}/"
echo ""


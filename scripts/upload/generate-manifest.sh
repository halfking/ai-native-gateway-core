#!/usr/bin/env bash
# scripts/upload/generate-manifest.sh - 生成版本清单
# 用法：bash generate-manifest.sh <build_output_dir> <version>

set -euo pipefail

# ============================================================================
# 配置
# ============================================================================

BUILD_OUTPUT="${1:?Usage: $0 <build_output_dir> <version>}"
VERSION="${2:?Usage: $0 <build_output_dir> <version>}"

ARCHIVES_DIR="${BUILD_OUTPUT}/archives"
MANIFEST_FILE="${BUILD_OUTPUT}/manifests/RELEASE-${VERSION}.json"

if [[ ! -d "$ARCHIVES_DIR" ]]; then
    echo "❌ 目录不存在: $ARCHIVES_DIR" >&2
    exit 1
fi

echo "========================================="
echo "生成版本清单"
echo "========================================="
echo ""
echo "版本: $VERSION"
echo "构建输出: $BUILD_OUTPUT"
echo ""

# ============================================================================
# 收集文件信息
# ============================================================================

echo "📦 收集文件信息..."

FILES_JSON="[]"

for archive in "${ARCHIVES_DIR}"/*.tar.gz "${ARCHIVES_DIR}"/*.zip; do
    if [[ ! -f "$archive" ]]; then
        continue
    fi
    
    filename=$(basename "$archive")
    filesize=$(stat -f%z "$archive" 2>/dev/null || stat -c%s "$archive" 2>/dev/null)
    sha256=$(sha256sum "$archive" | awk '{print $1}')
    
    # 解析文件名获取平台信息
    if [[ "$filename" =~ docker ]]; then
        platform="docker"
        os="linux"
        arch="amd64,arm64"
    elif [[ "$filename" =~ linux-amd64 ]]; then
        platform="host"
        os="linux"
        arch="amd64"
    elif [[ "$filename" =~ linux-arm64 ]]; then
        platform="host"
        os="linux"
        arch="arm64"
    elif [[ "$filename" =~ darwin-amd64 ]]; then
        platform="host"
        os="darwin"
        arch="amd64"
    elif [[ "$filename" =~ darwin-arm64 ]]; then
        platform="host"
        os="darwin"
        arch="arm64"
    elif [[ "$filename" =~ windows-amd64 ]]; then
        platform="host"
        os="windows"
        arch="amd64"
    else
        platform="host"
        os="linux"
        arch="amd64"
    fi
    
    # 添加到 JSON 数组
    FILES_JSON=$(echo "$FILES_JSON" | jq \
        --arg filename "$filename" \
        --arg filesize "$filesize" \
        --arg sha256 "$sha256" \
        --arg platform "$platform" \
        --arg os "$os" \
        --arg arch "$arch" \
        '. += [{
            "filename": $filename,
            "size": ($filesize | tonumber),
            "sha256": $sha256,
            "platform": $platform,
            "os": $os,
            "arch": $arch,
            "download_url": ""
        }]')
    
    echo "   ✅ $filename"
done

# ============================================================================
# 读取版本信息
# ============================================================================

echo ""
echo "📋 读取版本信息..."

# 优先从version.json读取
VERSION_FILE="${BUILD_OUTPUT}/../version.json"
if [[ -f "$VERSION_FILE" ]]; then
    GIT_TAG=$(jq -r '.git_tag // .version' "$VERSION_FILE" | head -1 | cut -d'-' -f1)
    BUILD_SEQ=$(jq -r '.build_seq // 0' "$VERSION_FILE")
    GIT_SHA=$(jq -r '.git_sha // "unknown"' "$VERSION_FILE")
    BUILD_DATE=$(jq -r '.build_date // ""' "$VERSION_FILE")
else
    # 从VERSION字符串解析（使用"-"分割）
    GIT_TAG=$(echo "$VERSION" | cut -d'-' -f1)
    # 尝试从第2个字段提取build_seq
    BUILD_SEQ_STR=$(echo "$VERSION" | cut -d'-' -f2)
    if [[ "$BUILD_SEQ_STR" =~ ^[0-9]+$ ]]; then
        BUILD_SEQ=$BUILD_SEQ_STR
    else
        BUILD_SEQ=0
    fi
    GIT_SHA=$(echo "$VERSION" | cut -d'-' -f3)
    BUILD_DATE=$(echo "$VERSION" | cut -d'-' -f4)
fi

# 默认值处理
GIT_TAG=${GIT_TAG:-unknown}
BUILD_SEQ=${BUILD_SEQ:-0}
GIT_SHA=${GIT_SHA:-unknown}
BUILD_DATE=${BUILD_DATE:-$(date +%Y%m%d)}

echo "   版本: $GIT_TAG"
echo "   构建序号: $BUILD_SEQ"
echo "   Git SHA: $GIT_SHA"
echo "   构建日期: $BUILD_DATE"

# ============================================================================
# 生成清单
# ============================================================================

echo ""
echo "📝 生成清单文件..."

mkdir -p "$(dirname "$MANIFEST_FILE")"

cat > "$MANIFEST_FILE" << JSON
{
  "version": "$GIT_TAG",
  "full_version": "$VERSION",
  "build_seq": $BUILD_SEQ,
  "git_sha": "$GIT_SHA",
  "build_date": "$BUILD_DATE",
  "release_date": "$(date -u +%Y-%m-%dT%H:%M:%SZ)",
  "release_type": "stable",
  "changelog_url": "https://docs.kxpms.cn/llm-gateway-go/changelog/${GIT_TAG}",
  "files": $(echo "$FILES_JSON" | jq -c .),
  "requirements": {
    "min_postgres_version": "14",
    "min_redis_version": "6",
    "min_go_version": "1.21",
    "min_node_version": "18"
  },
  "compatibility": {
    "upgrade_from": ["2.4.6", "2.4.5"],
    "breaking_changes": []
  },
  "metadata": {
    "total_files": $(echo "$FILES_JSON" | jq 'length'),
    "total_size": $(echo "$FILES_JSON" | jq '[.[].size] | add'),
    "platforms": $(echo "$FILES_JSON" | jq -c '[.[].platform] | unique'),
    "architectures": $(echo "$FILES_JSON" | jq -c '[.[].arch] | unique')
  }
}
JSON

# 美化输出
jq '.' "$MANIFEST_FILE" > "${MANIFEST_FILE}.tmp"
mv "${MANIFEST_FILE}.tmp" "$MANIFEST_FILE"

echo "   ✅ 清单已生成: $MANIFEST_FILE"

# ============================================================================
# 汇总
# ============================================================================

echo ""
echo "========================================="
echo "✅ 清单生成完成"
echo "========================================="
echo ""
echo "文件:"
echo "  JSON: $MANIFEST_FILE"
echo ""
echo "统计:"
echo "  文件数: $(echo "$FILES_JSON" | jq 'length')"
echo ""


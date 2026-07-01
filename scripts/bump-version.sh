#!/usr/bin/env bash
# =====================================================================
# bump-version.sh — 版本号管理和编译次数自动增长脚本
#
# 功能：
# 1. 从git获取最近的tag作为版本号
# 2. 读取version.json中的build_seq并自动+1
# 3. 更新version.json和VERSION文件
# 4. 返回版本信息供Docker构建使用
#
# 用法: 
#   source scripts/bump-version.sh
#   或
#   ./scripts/bump-version.sh
# =====================================================================

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
VERSION_JSON="${PROJECT_ROOT}/version.json"
VERSION_FILE="${PROJECT_ROOT}/VERSION"

# ── 获取git信息 ────────────────────────────────────────────────────
cd "${PROJECT_ROOT}"

# 获取最近的tag作为版本号
GIT_TAG=$(git describe --tags --abbrev=0 2>/dev/null || echo "v0.0.0")
echo "📌 Git tag: ${GIT_TAG}"

# 获取当前commit的短hash
GIT_SHA=$(git rev-parse --short HEAD)
echo "📌 Git SHA: ${GIT_SHA}"

# 获取当前日期
BUILD_DATE=$(date +%Y%m%d)
echo "📌 Build date: ${BUILD_DATE}"

# ── 读取并更新build_seq ──────────────────────────────────────────
if [[ ! -f "${VERSION_JSON}" ]]; then
  echo "⚠️  version.json不存在，创建初始文件"
  cat > "${VERSION_JSON}" <<EOF
{
  "version": "${GIT_TAG}",
  "git_tag": "${GIT_TAG}",
  "git_sha": "${GIT_SHA}",
  "build_seq": 0,
  "build_date": "${BUILD_DATE}",
  "module": "llm-gateway-go"
}
EOF
  BUILD_SEQ=0
else
  # 读取当前build_seq
  BUILD_SEQ=$(grep -o '"build_seq"[[:space:]]*:[[:space:]]*[0-9]*' "${VERSION_JSON}" | grep -o '[0-9]*$' || echo "0")
fi

# 自增build_seq
BUILD_SEQ=$((BUILD_SEQ + 1))
echo "📌 Build sequence: ${BUILD_SEQ}"

# ── 更新version.json ──────────────────────────────────────────────
cat > "${VERSION_JSON}" <<EOF
{
  "version": "${GIT_TAG}",
  "git_tag": "${GIT_TAG}",
  "git_sha": "${GIT_SHA}",
  "build_seq": ${BUILD_SEQ},
  "build_date": "${BUILD_DATE}",
  "module": "llm-gateway-go"
}
EOF

echo "✅ 已更新 ${VERSION_JSON}"

# ── 更新VERSION文件 ───────────────────────────────────────────────
VERSION_STRING="${GIT_TAG}-${GIT_SHA}-${BUILD_DATE}-${BUILD_SEQ}"
echo "${VERSION_STRING}" > "${VERSION_FILE}"
echo "✅ 已更新 ${VERSION_FILE}: ${VERSION_STRING}"

# ── 导出环境变量供后续使用 ────────────────────────────────────────
export BUILD_VERSION="${GIT_TAG}"
export BUILD_GIT_TAG="${GIT_TAG}"
export BUILD_GIT_SHA="${GIT_SHA}"
export BUILD_DATE="${BUILD_DATE}"
export BUILD_SEQ="${BUILD_SEQ}"
export VERSION_STRING="${VERSION_STRING}"

echo ""
echo "🎉 版本信息已更新："
echo "   Version: ${GIT_TAG}"
echo "   Git SHA: ${GIT_SHA}"
echo "   Build Seq: ${BUILD_SEQ}"
echo "   Build Date: ${BUILD_DATE}"
echo "   Full Version: ${VERSION_STRING}"
echo ""
echo "环境变量已导出："
echo "   BUILD_VERSION=${BUILD_VERSION}"
echo "   BUILD_GIT_TAG=${BUILD_GIT_TAG}"
echo "   BUILD_GIT_SHA=${BUILD_GIT_SHA}"
echo "   BUILD_DATE=${BUILD_DATE}"
echo "   BUILD_SEQ=${BUILD_SEQ}"
echo "   VERSION_STRING=${VERSION_STRING}"

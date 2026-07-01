#!/usr/bin/env bash
# =====================================================================
# build-image.sh — llm-gateway-go 镜像构建脚本（rule 22 §6）
#
# 用法: ./deploy/build-image.sh [--tag <tag>] [--push]
#
# 功能：
# 1. 自动调用scripts/bump-version.sh更新版本号和build_seq
# 2. 将版本信息注入到Docker镜像构建过程
# 3. 支持推送到registry
# =====================================================================

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
SERVICE_NAME="llm-gateway-go"
REGISTRY="${REGISTRY:-registry.internal.example.com}"
IMAGE_NAME="${REGISTRY}/kaixuan-platform-${SERVICE_NAME}"

# ── 解析参数 ───────────────────────────────────────────────────────
TAG=""
PUSH=false

while [[ $# -gt 0 ]]; do
  case "$1" in
    --tag) TAG="$2"; shift 2 ;;
    --push) PUSH=true; shift ;;
    --help) echo "用法: $0 [--tag <tag>] [--push]"; exit 0 ;;
    *) echo "未知参数: $1"; exit 1 ;;
  esac
done

# ── 自动更新版本信息 ──────────────────────────────────────────────
echo "=== 更新版本信息 ==="
source "${PROJECT_ROOT}/scripts/bump-version.sh"

# 如果没有指定tag，使用版本字符串作为tag
if [[ -z "${TAG}" ]]; then
  TAG="${VERSION_STRING}"
fi

echo ""
echo "=== build-image: ${SERVICE_NAME} ==="
echo "  镜像: ${IMAGE_NAME}:${TAG}"
echo "  版本: ${BUILD_VERSION}"
echo "  Git SHA: ${BUILD_GIT_SHA}"
echo "  Build Seq: ${BUILD_SEQ}"
echo "  Build Date: ${BUILD_DATE}"
echo "  推送: ${PUSH}"

# ── 检查 Docker 可用 ──────────────────────────────────────────────
command -v docker >/dev/null 2>&1 || { echo "❌ docker 未安装"; exit 1; }

# ── 构建镜像（多架构支持） ─────────────────────────────────────────
BUILD_ARGS=(
  --platform linux/amd64
  --build-arg REGISTRY="${REGISTRY}"
  --build-arg GIT_TAG="${BUILD_GIT_TAG}"
  --build-arg GIT_SHA="${BUILD_GIT_SHA}"
  --build-arg BUILD_DATE="${BUILD_DATE}"
  --build-arg BUILD_SEQ="${BUILD_SEQ}"
  -t "${IMAGE_NAME}:${TAG}"
  -t "${IMAGE_NAME}:latest"
  -f "${SCRIPT_DIR}/../Dockerfile"
  "${SCRIPT_DIR}/.."
)

echo ""
echo "▶ docker build ${BUILD_ARGS[*]}"
docker build "${BUILD_ARGS[@]}"

# ── 可选：推送镜像 ─────────────────────────────────────────────────
if [[ "$PUSH" == true ]]; then
  echo "▶ docker push ${IMAGE_NAME}:${TAG}"
  docker push "${IMAGE_NAME}:${TAG}"
  echo "▶ docker push ${IMAGE_NAME}:latest"
  docker push "${IMAGE_NAME}:latest"
fi

echo ""
echo "✅ build-image 完成: ${IMAGE_NAME}:${TAG}"
echo "   版本信息已注入镜像"

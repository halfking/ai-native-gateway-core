#!/usr/bin/env bash
# =====================================================================
# build-image.sh — llm-gateway-go 镜像构建脚本（rule 22 §6）
#
# 用法: ./deploy/build-image.sh [--tag <tag>] [--push]
# =====================================================================

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SERVICE_NAME="llm-gateway-go"  # 替换为实际服务名
REGISTRY="${REGISTRY:-registry.internal.example.com}"
IMAGE_NAME="${REGISTRY}/kaixuan-platform-${SERVICE_NAME}"

# ── 解析参数 ───────────────────────────────────────────────────────
TAG="latest"
PUSH=false

while [[ $# -gt 0 ]]; do
  case "$1" in
    --tag) TAG="$2"; shift 2 ;;
    --push) PUSH=true; shift ;;
    --help) echo "用法: $0 [--tag <tag>] [--push]"; exit 0 ;;
    *) echo "未知参数: $1"; exit 1 ;;
  esac
done

echo "=== build-image: ${SERVICE_NAME} ==="
echo "  镜像: ${IMAGE_NAME}:${TAG}"
echo "  推送: ${PUSH}"

# ── 检查 Docker 可用 ──────────────────────────────────────────────
command -v docker >/dev/null 2>&1 || { echo "❌ docker 未安装"; exit 1; }

# ── 构建镜像（多架构支持） ─────────────────────────────────────────
BUILD_ARGS=(
  --platform linux/amd64
  --build-arg REGISTRY="${REGISTRY}"
  --build-arg SOURCE_VERSION="${TAG}"
  --build-arg CACHEBUST="$(date +%s)"
  -t "${IMAGE_NAME}:${TAG}"
  -f "${SCRIPT_DIR}/../Dockerfile"
  "${SCRIPT_DIR}/.."
)

echo "▶ docker build ${BUILD_ARGS[*]}"
docker build "${BUILD_ARGS[@]}"

# ── 可选：推送镜像 ─────────────────────────────────────────────────
if [[ "$PUSH" == true ]]; then
  echo "▶ docker push ${IMAGE_NAME}:${TAG}"
  docker push "${IMAGE_NAME}:${TAG}"
fi

echo "✅ build-image 完成: ${IMAGE_NAME}:${TAG}"

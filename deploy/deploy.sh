#!/usr/bin/env bash
# =====================================================================
# deploy.sh — llm-gateway-go 部署脚本（rule 22 §6.3）
#
# 用法: ./deploy/deploy.sh [--env local|prod] [--tag <tag>] [--dry-run]
# 自动验证: 部署后自动调用 verify.sh，失败触发 rollback
# =====================================================================

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SERVICE_NAME="llm-gateway-go"
CONTAINER_NAME="kx-${SERVICE_NAME}"

# ── 默认值 ─────────────────────────────────────────────────────────
ENV="local"
TAG="latest"
DRY_RUN=false
REGISTRY="${REGISTRY:-registry.internal.example.com}"

# ── 解析参数 ───────────────────────────────────────────────────────
while [[ $# -gt 0 ]]; do
  case "$1" in
    --env) ENV="$2"; shift 2 ;;
    --tag) TAG="$2"; shift 2 ;;
    --dry-run) DRY_RUN=true; shift ;;
    --help) echo "用法: $0 [--env local|prod] [--tag <tag>] [--dry-run]"; exit 0 ;;
    *) echo "未知参数: $1"; exit 1 ;;
  esac
done

echo "=== deploy: ${SERVICE_NAME} (env=${ENV}, tag=${TAG}) ==="

if [[ "$DRY_RUN" == true ]]; then
  echo "[DRY-RUN] 以下命令将被执行:"
  echo "  docker pull ${REGISTRY}/kaixuan-platform-${SERVICE_NAME}:${TAG}"
  echo "  docker stop ${CONTAINER_NAME} 2>/dev/null || true"
  echo "  docker rm ${CONTAINER_NAME} 2>/dev/null || true"
  echo "  docker run -d --name ${CONTAINER_NAME} ..."
  echo "[DRY-RUN] ✅ 完成（未实际执行）"
  exit 0
fi

# ── 1. 前置检查 ────────────────────────────────────────────────────
command -v docker >/dev/null 2>&1 || { echo "❌ docker 未安装"; exit 1; }

# ── 2. 拉取镜像 ────────────────────────────────────────────────────
echo "▶ 拉取镜像: ${REGISTRY}/kaixuan-platform-${SERVICE_NAME}:${TAG}"
# TODO: 根据环境选择性推送/拉取
# docker pull ${REGISTRY}/kaixuan-platform-${SERVICE_NAME}:${TAG}"

# ── 3. 保存当前版本信息（用于回滚） ────────────────────────────────
DEPLOY_TRACKER_DIR="/var/lib/deploy-tracker"
mkdir -p "${DEPLOY_TRACKER_DIR}"
# 记录当前运行容器的镜像 tag
# docker inspect ${CONTAINER_NAME} --format '{{.Config.Image}}' 2>/dev/null \
#   | sed 's/.*://' > "${DEPLOY_TRACKER_DIR}/${SERVICE_NAME}_prev_tag" || true
echo "${TAG}" > "${DEPLOY_TRACKER_DIR}/${SERVICE_NAME}_current_tag"

# ── 4. 停止旧容器 ──────────────────────────────────────────────────
echo "▶ 停止旧容器: ${CONTAINER_NAME}"
docker stop "${CONTAINER_NAME}" 2>/dev/null || true
docker rm "${CONTAINER_NAME}" 2>/dev/null || true

# ── 5. 启动新容器 ──────────────────────────────────────────────────
echo "▶ 启动新容器: ${CONTAINER_NAME}"
# TODO: 替换为实际的 docker run 命令
# 镜像名称需替换为实际镜像
# docker run -d \
#   --name "${CONTAINER_NAME}" \
#   --restart always \
#   -p ${PORT:-8080}:${PORT:-8080} \
#   --env-file .env \
#   --network kaixuan_local_net \
#   "${REGISTRY}/kaixuan-platform-${SERVICE_NAME}:${TAG}"

echo "✅ 部署完成"

# ── 6. 自动验证 ────────────────────────────────────────────────────
echo "▶ 运行验证..."
if "${SCRIPT_DIR}/verify.sh" --env "${ENV}" --tag "${TAG}"; then
  echo "✅ 验证通过"
else
  echo "⚠️  验证失败，触发回滚..."
  "${SCRIPT_DIR}/rollback.sh" --env "${ENV}"
  exit 1
fi

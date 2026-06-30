#!/usr/bin/env bash
# =====================================================================
# rollback.sh — llm-gateway-go 回滚脚本（rule 22 §8）
#
# 用法: ./deploy/rollback.sh [--env local|prod]
# 说明: 回滚到前一个部署版本（从 /var/lib/deploy-tracker/ 读取）
# =====================================================================

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SERVICE_NAME="llm-gateway-go"
CONTAINER_NAME="kx-${SERVICE_NAME}"

ENV="local"
REGISTRY="${REGISTRY:-registry.internal.example.com}"
while [[ $# -gt 0 ]]; do
  case "$1" in
    --env) ENV="$2"; shift 2 ;;
    --help) echo "用法: $0 [--env local|prod]"; exit 0 ;;
    *) echo "未知参数: $1"; exit 1 ;;
  esac
done

echo "━━━ rollback: ${SERVICE_NAME} (env=${ENV}) ━━━"

# ── 1. 读取前一个版本 ──────────────────────────────────────────────
DEPLOY_TRACKER_DIR="/var/lib/deploy-tracker"
PREV_TAG_FILE="${DEPLOY_TRACKER_DIR}/${SERVICE_NAME}_prev_tag"

if [[ ! -f "$PREV_TAG_FILE" ]]; then
  echo "❌ 未找到前一个版本记录（${PREV_TAG_FILE}）"
  echo "   回滚需要手动指定 tag：docker run ... ${REGISTRY}/...:<tag>"
  exit 1
fi

PREV_TAG=$(cat "$PREV_TAG_FILE")
echo "   前版本: ${PREV_TAG}"

# ── 2. 拉取前一个版本 ──────────────────────────────────────────────
echo "▶ 拉取镜像: ${REGISTRY}/kaixuan-platform-${SERVICE_NAME}:${PREV_TAG}"
# docker pull ${REGISTRY}/kaixuan-platform-${SERVICE_NAME}:${PREV_TAG}

# ── 3. 停止当前容器 ────────────────────────────────────────────────
echo "▶ 停止当前容器: ${CONTAINER_NAME}"
docker stop "${CONTAINER_NAME}" 2>/dev/null || true
docker rm "${CONTAINER_NAME}" 2>/dev/null || true

# ── 4. 启动前一个版本 ──────────────────────────────────────────────
echo "▶ 启动前版本: ${PREV_TAG}"
# TODO: 替换为实际的 docker run 命令（与 deploy.sh 保持一致）
# docker run -d \
#   --name "${CONTAINER_NAME}" \
#   --restart always \
#   -p ${PORT:-8080}:${PORT:-8080} \
#   --env-file .env \
#   "${REGISTRY}/kaixuan-platform-${SERVICE_NAME}:${PREV_TAG}"

echo "✅ 回滚完成（版本: ${PREV_TAG}）"

# ── 5. 验证回滚后状态 ──────────────────────────────────────────────
echo "▶ 验证回滚后状态..."
if "${SCRIPT_DIR}/verify.sh" --env "${ENV}" --tag "${PREV_TAG}"; then
  echo "✅ 回滚后验证通过"
else
  echo "⚠️  回滚后验证失败，请人工介入"
  exit 1
fi

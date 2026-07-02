#!/usr/bin/env bash
# =====================================================================
# push-to-internal-registry.sh — 仅推送镜像到内部 registry（不打 tarball）
#
# 用法: ./packaging/push-to-internal-registry.sh <version>
# =====================================================================

set -euo pipefail

VERSION="${1:?Usage: $0 <version>}"
REGISTRY="${REGISTRY:-registry.kxpms.cn}"
PROJECT="${PROJECT:-kaixuan}"
REGISTRY_USERNAME="${REGISTRY_USERNAME:-}"
REGISTRY_PASSWORD="${REGISTRY_PASSWORD:-}"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"

APP_IMG="kx-llm-gateway-go:${VERSION}"
DB_IMG="citusdata/citus:11.3.0"
REDIS_IMG="redis:7-alpine"

echo "═══ 推送 3 个镜像到 ${REGISTRY}/${PROJECT} ═══"

# 准备应用镜像
echo "▶ 构建应用镜像 ${APP_IMG} ..."
cd "${PROJECT_ROOT}"
docker build -t "${APP_IMG}" .

# 拉取依赖镜像
echo "▶ 拉取数据库镜像 ${DB_IMG} ..."
docker pull "${DB_IMG}"
echo "▶ 拉取 Redis 镜像 ${REDIS_IMG} ..."
docker pull "${REDIS_IMG}"

# 登录（如有凭证）
if [[ -n "${REGISTRY_USERNAME}" ]]; then
  echo "▶ docker login ${REGISTRY} ..."
  docker login "${REGISTRY}" -u "${REGISTRY_USERNAME}" -p "${REGISTRY_PASSWORD}"
fi

# 推送
for img in "${APP_IMG}" "${DB_IMG}" "${REDIS_IMG}"; do
  dst="${REGISTRY}/${PROJECT}/${img}"
  echo ""
  echo "▶ docker tag ${img} ${dst}"
  docker tag "${img}" "${dst}"
  echo "▶ docker push ${dst}"
  docker push "${dst}"
  echo "  ✅ ${dst}"
done

echo ""
echo "═══════════════════════════════════════════════════════"
echo "  ✅ 推送完成"
echo "═══════════════════════════════════════════════════════"

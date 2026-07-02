#!/usr/bin/env bash
# =====================================================================
# package.sh — 在构建机上产出 llm-gateway-go release tarball
#
# 用法: ./packaging/package.sh <version> [options]
#   <version>            e.g. v1.0.0
#
# 选项 (环境变量):
#   REGISTRY             内部 registry 地址 (默认 registry.kxpms.cn)
#   PROJECT              registry 项目路径 (默认 kaixuan)
#   REGISTRY_USERNAME    registry 用户名 (可选)
#   REGISTRY_PASSWORD    registry 密码 (可选)
#   SKIP_PUSH            设为 1 跳过推送到 registry
#   SKIP_OFFLINE         设为 1 跳过离线 tarball (仅推 registry)
# =====================================================================

set -euo pipefail

# ── 参数解析 ──────────────────────────────────────────────────────
VERSION="${1:-}"
if [[ -z "${VERSION}" ]]; then
  echo "用法: $0 <version> [REGISTRY=...]"
  echo "示例: $0 v1.0.0"
  exit 1
fi

REGISTRY="${REGISTRY:-registry.kxpms.cn}"
PROJECT="${PROJECT:-kaixuan}"
SKIP_PUSH="${SKIP_PUSH:-0}"
SKIP_OFFLINE="${SKIP_OFFLINE:-0}"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
BUILD_DIR="${PROJECT_ROOT}/release-${VERSION}"

# ── 1. 准备目录 ──────────────────────────────────────────────────
echo ""
echo "═══ 1. 准备目录 ${BUILD_DIR} ═══"
rm -rf "${BUILD_DIR}"
mkdir -p "${BUILD_DIR}/images" "${BUILD_DIR}/sql"

# ── 2. 构建/拉取所有镜像 ────────────────────────────────────────
echo ""
echo "═══ 2. 准备 3 个镜像 ═══"

# 2.1 应用镜像：本地 docker build
APP_IMG="kx-llm-gateway-go:${VERSION}"
echo "  ▶ 构建应用镜像 ${APP_IMG} ..."
cd "${PROJECT_ROOT}"
docker build -t "${APP_IMG}" .

# 2.2 数据库镜像
DB_IMG="citusdata/citus:11.3.0"
echo "  ▶ 拉取数据库镜像 ${DB_IMG} ..."
docker pull "${DB_IMG}"

# 2.3 Redis 镜像
REDIS_IMG="redis:7-alpine"
echo "  ▶ 拉取 Redis 镜像 ${REDIS_IMG} ..."
docker pull "${REDIS_IMG}"

# ── 3. 推送到内部 registry（⭐ 关键） ─────────────────────────────
if [[ "${SKIP_PUSH}" == "0" ]]; then
  echo ""
  echo "═══ 3. 推送到内部 registry ${REGISTRY}/${PROJECT} ═══"
  for img in "${APP_IMG}" "${DB_IMG}" "${REDIS_IMG}"; do
    dst="${REGISTRY}/${PROJECT}/${img}"
    echo "  ▶ docker tag ${img} ${dst}"
    docker tag "${img}" "${dst}"

    echo "  ▶ docker push ${dst}"
    if [[ -n "${REGISTRY_USERNAME:-}" ]]; then
      docker login "${REGISTRY}" -u "${REGISTRY_USERNAME}" -p "${REGISTRY_PASSWORD:-}"
    fi
    docker push "${dst}"
    echo "  ✅ pushed: ${dst}"
  done
else
  echo ""
  echo "═══ 3. 跳过推送 (SKIP_PUSH=1) ═══"
fi

# ── 4. 保存离线 tarball ────────────────────────────────────────
if [[ "${SKIP_OFFLINE}" == "0" ]]; then
  echo ""
  echo "═══ 4. 保存离线 tarball 到 ${BUILD_DIR}/images ═══"
  docker save "${APP_IMG}"  | gzip > "${BUILD_DIR}/images/kx-llm-gateway-go-${VERSION}.tar.gz"
  docker save "${DB_IMG}"   | gzip > "${BUILD_DIR}/images/kx-citus-v11.3.0.tar.gz"
  docker save "${REDIS_IMG}" | gzip > "${BUILD_DIR}/images/kx-redis-v7-alpine.tar.gz"
  ls -lh "${BUILD_DIR}/images/"
else
  echo ""
  echo "═══ 4. 跳过离线 tarball (SKIP_OFFLINE=1) ═══"
fi

# ── 5. 复制 SQL 文件 ────────────────────────────────────────────
echo ""
echo "═══ 5. 复制 SQL 文件 ═══"
cp "${PROJECT_ROOT}/deploy/sql/00-prereqs.sql" "${BUILD_DIR}/sql/"
cp "${PROJECT_ROOT}/deploy/sql/01-schema.sql"  "${BUILD_DIR}/sql/"
cp "${PROJECT_ROOT}/deploy/sql/02-seed.sql"    "${BUILD_DIR}/sql/"
ls -lh "${BUILD_DIR}/sql/"

# ── 6. 复制 installer 二进制 ────────────────────────────────────
echo ""
echo "═══ 6. 复制 installer 二进制 ═══"
DIST_DIR="${PROJECT_ROOT}/dist"
if [[ -d "${DIST_DIR}" ]]; then
  cp "${DIST_DIR}"/llm-gw-installer-* "${BUILD_DIR}/" 2>/dev/null || true
fi
# 兜底：用当前架构的二进制
if [[ ! -f "${BUILD_DIR}/llm-gw-installer" ]] && command -v go >/dev/null; then
  echo "  ▶ 跨平台编译 installer 二进制 ..."
  cd "${PROJECT_ROOT}/installer"
  for target in linux-amd64 linux-arm64 linux-loong64 \
                darwin-amd64 darwin-arm64 \
                windows-amd64 windows-arm64; do
    os="${target%%-*}"
    arch="${target##*-}"
    ext=""
    [[ "${os}" == "windows" ]] && ext=".exe"
    out="${BUILD_DIR}/llm-gw-installer-${target}${ext}"
    GOOS="${os}" GOARCH="${arch}" GOPROXY=https://goproxy.cn,direct \
      go build -ldflags="-s -w" -o "${out}" ./cmd/llm-gw-installer/ 2>/dev/null || \
      echo "  ⚠️  ${target} 编译失败，跳过"
    if [[ -f "${out}" ]]; then
      echo "  ✅ ${out}"
    fi
  done
  cd "${PROJECT_ROOT}"
fi

# ── 7. 复制 shell/PowerShell 入口 ────────────────────────────────
echo ""
echo "═══ 7. 复制入口脚本 ═══"
for f in install.sh install.ps1 install.bat uninstall.sh; do
  if [[ -f "${SCRIPT_DIR}/${f}" ]]; then
    cp "${SCRIPT_DIR}/${f}" "${BUILD_DIR}/"
  fi
done

# ── 8. 复制 README ───────────────────────────────────────────────
if [[ -f "${SCRIPT_DIR}/README.md" ]]; then
  cp "${SCRIPT_DIR}/README.md" "${BUILD_DIR}/"
fi

# ── 9. 生成 MANIFEST.json ───────────────────────────────────────
echo ""
echo "═══ 9. 生成 MANIFEST.json ═══"
bash "${SCRIPT_DIR}/gen-manifest.sh" "${BUILD_DIR}" "${REGISTRY}" "${PROJECT}" > "${BUILD_DIR}/MANIFEST.json"
cat "${BUILD_DIR}/MANIFEST.json"

# ── 10. 计算 SHA256 ─────────────────────────────────────────────
echo ""
echo "═══ 10. 计算 SHA256 ═══"
cd "${BUILD_DIR}"
find . -type f -exec sha256sum {} \; > checksums.sha256
cd - > /dev/null

# ── 11. 打 tarball ──────────────────────────────────────────────
echo ""
echo "═══ 11. 打 tarball ═══"
cd "${PROJECT_ROOT}"
tar czf "release-${VERSION}.tar.gz" "release-${VERSION}/"
SIZE=$(ls -lh "release-${VERSION}.tar.gz" | awk '{print $5}')
echo ""
echo "═══════════════════════════════════════════════════════"
echo "  ✅ 完成: release-${VERSION}.tar.gz (${SIZE})"
echo "═══════════════════════════════════════════════════════"
echo ""
echo "内容:"
ls -lh "release-${VERSION}/"
echo ""
echo "下一步:"
echo "  1. 将 release-${VERSION}.tar.gz 拷贝到客户机器"
echo "  2. 客户解压后执行: tar xzf release-${VERSION}.tar.gz && cd release-${VERSION} && ./install.sh"

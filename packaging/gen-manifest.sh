#!/usr/bin/env bash
# gen-manifest.sh — 生成 MANIFEST.json
# 用法: gen-manifest.sh <build_dir> <registry> <project>

set -euo pipefail

BUILD_DIR="${1:?Usage: gen-manifest.sh <build_dir> <registry> <project>}"
REGISTRY="${2:-registry.kxpms.cn}"
PROJECT="${3:-kaixuan}"

VERSION="$(basename "${BUILD_DIR}" | sed 's/^release-//')"
GIT_SHA="$(git -C "$(dirname "${BUILD_DIR}")" rev-parse --short=8 HEAD 2>/dev/null || echo "unknown")"
BUILD_DATE="$(date -u +%Y%m%d)"

# 计算文件 SHA256
sha256_file() {
  if [[ -f "$1" ]]; then
    sha256sum "$1" | awk '{print $1}'
  else
    echo ""
  fi
}

# 文件大小（MB）
size_mb() {
  if [[ -f "$1" ]]; then
    local bytes
    bytes=$(stat -c%s "$1" 2>/dev/null || stat -f%z "$1" 2>/dev/null || echo 0)
    echo $((bytes / 1024 / 1024))
  else
    echo 0
  fi
}

cat <<EOF
{
  "package_name": "kx-llm-gateway-go",
  "version": "${VERSION}",
  "build_seq": $(cat "$(dirname "${BUILD_DIR}")/build_seq" 2>/dev/null || echo 0),
  "git_sha": "${GIT_SHA}",
  "build_date": "${BUILD_DATE}",
  "registry": "${REGISTRY}",
  "project": "${PROJECT}",
  "images": [
    {
      "name": "kx-llm-gateway-go",
      "tag": "${VERSION}",
      "source": "kx-llm-gateway-go:${VERSION}",
      "internal_registry": "${REGISTRY}/${PROJECT}/kx-llm-gateway-go:${VERSION}",
      "offline_tarball": "images/kx-llm-gateway-go-${VERSION}.tar.gz",
      "size_mb": $(size_mb "${BUILD_DIR}/images/kx-llm-gateway-go-${VERSION}.tar.gz"),
      "sha256": "$(sha256_file "${BUILD_DIR}/images/kx-llm-gateway-go-${VERSION}.tar.gz")"
    },
    {
      "name": "kx-citus",
      "tag": "v11.3.0",
      "source": "citusdata/citus:11.3.0",
      "internal_registry": "${REGISTRY}/${PROJECT}/citusdata/citus:11.3.0",
      "offline_tarball": "images/kx-citus-v11.3.0.tar.gz",
      "size_mb": $(size_mb "${BUILD_DIR}/images/kx-citus-v11.3.0.tar.gz"),
      "sha256": "$(sha256_file "${BUILD_DIR}/images/kx-citus-v11.3.0.tar.gz")"
    },
    {
      "name": "kx-redis",
      "tag": "v7-alpine",
      "source": "redis:7-alpine",
      "internal_registry": "${REGISTRY}/${PROJECT}/redis:7-alpine",
      "offline_tarball": "images/kx-redis-v7-alpine.tar.gz",
      "size_mb": $(size_mb "${BUILD_DIR}/images/kx-redis-v7-alpine.tar.gz"),
      "sha256": "$(sha256_file "${BUILD_DIR}/images/kx-redis-v7-alpine.tar.gz")"
    }
  ],
  "fallback_chain": [
    "offline-tarball",
    "internal-registry:${REGISTRY}",
    "aliyun-mirror:registry.cn-hangzhou.aliyuncs.com",
    "docker-hub:registry-1.docker.io"
  ],
  "minimum_requirements": {
    "docker_version": ">=20.10",
    "disk_free_gb": 5,
    "ram_mb": 2048
  }
}
EOF

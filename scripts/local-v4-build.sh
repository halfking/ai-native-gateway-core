#!/usr/bin/env bash
# 会话优化 v4 — 本地构建 + 部署脚本（macOS arm64 宿主）
#
# 流程:
#   1. 宿主编译 linux/arm64 二进制（-mod=vendor，免拉构建镜像）
#   2. 用仓库 Dockerfile.local-arm64 打运行时镜像
#      （基础镜像 registry.kxpms.cn/kx-base:go-vue-alpine-slim-v2，
#        来源 ~/work/docker-base-images/v2-saved/kx-base_go-vue-alpine-slim-v2.tar.gz）
#   3. docker compose -f docker-compose.v4-local.yml up -d 部署
#
# 依赖的宿主服务实例（不重建）: llm-gateway-pg (citus/columnar)、nbjl-redis。
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT"

# Load shared local service credentials when present. The file is ignored by
# git and must remain local-only; compose enforces the gateway password.
if [ -f .env.local ]; then
  set -a
  # shellcheck disable=SC1091
  source .env.local
  set +a
fi

: "${LLM_GATEWAY_DB_PASSWORD:?set LLM_GATEWAY_DB_PASSWORD or create .env.local}"

IMAGE_TAG="llm-gateway-go:v4-local"

echo "==> [1/3] 编译 linux/arm64 二进制 (vendor 模式)"
mkdir -p .build-local
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -mod=vendor \
  -ldflags="-s -w" -o .build-local/llm-gateway-go ./cmd/gateway

if [ ! -f web/dist/index.html ]; then
  echo "!! web/dist 缺失：请在 web/ 下执行 npm ci && npm run build 后重试" >&2
  exit 1
fi

echo "==> [2/3] 构建镜像 $IMAGE_TAG"
docker build -t "$IMAGE_TAG" -f Dockerfile.local-arm64 .

echo "==> [3/3] 部署 (docker-compose.v4-local.yml)"
docker compose -f docker-compose.v4-local.yml up -d

echo "==> 完成。健康检查: curl http://127.0.0.1:18781/healthz"

#!/usr/bin/env bash
# ─────────────────────────────────────────────────────────────────────
# R1.12 本地测试环境启动脚本
#
# 流程:
#   1. docker-compose up (postgres, redis, memora-mcp, llm-mock)
#   2. 等待 postgres / redis 健康
#   3. 应用 PG migrations (call local-r112-migrate.sh)
#   4. 启动 gateway-v2
#   5. 等待 gateway-v2 /healthz
#
# 用法:
#   ./scripts/local-r112-up.sh
#
# 验证:
#   bash -n scripts/local-r112-up.sh
# ─────────────────────────────────────────────────────────────────────

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
COMPOSE_FILE="$ROOT_DIR/docker-compose.local-r112.yml"

# ── 颜色 (失败时建议醒目) ──
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

err()  { echo -e "${RED}✗ $*${NC}" >&2; }
ok()   { echo -e "${GREEN}✓ $*${NC}"; }
info() { echo -e "${YELLOW}▶ $*${NC}"; }

# ── 前置检查 ──
command -v docker >/dev/null 2>&1 || { err "docker 未安装"; exit 1; }
command -v docker-compose >/dev/null 2>&1 || docker compose version >/dev/null 2>&1 || {
  err "docker-compose 未安装"; exit 1;
}

# 选择 compose 命令 (兼容 v1/v2)
if docker compose version >/dev/null 2>&1; then
  COMPOSE_CMD="docker compose"
else
  COMPOSE_CMD="docker-compose"
fi

cd "$ROOT_DIR"

# ── 1. 启动依赖栈 (postgres / redis / memora-mcp / llm-mock) ──
info "启动依赖栈 (postgres, redis, memora-mcp, llm-mock)..."
$COMPOSE_CMD -f "$COMPOSE_FILE" up -d postgres redis memora-mcp llm-mock

# ── 2. 等待 postgres 健康 ──
info "等待 postgres (max 60s)..."
PG_OK=0
for i in $(seq 1 60); do
  if docker exec r112_postgres pg_isready -U kxuser -d postgres >/dev/null 2>&1; then
    PG_OK=1
    ok "postgres ready (after ${i}s)"
    break
  fi
  sleep 1
done
if [ "$PG_OK" -ne 1 ]; then
  err "postgres 60s 内未就绪"
  err "  修复建议: docker logs r112_postgres"
  err "  或: $COMPOSE_CMD -f $COMPOSE_FILE down -v  # 清 volumes 重试"
  exit 1
fi

# ── 3. 等待 redis 健康 ──
info "等待 redis (max 30s)..."
REDIS_OK=0
for i in $(seq 1 30); do
  if docker exec r112_redis redis-cli ping 2>/dev/null | grep -q PONG; then
    REDIS_OK=1
    ok "redis ready (after ${i}s)"
    break
  fi
  sleep 1
done
if [ "$REDIS_OK" -ne 1 ]; then
  err "redis 30s 内未就绪"
  err "  修复建议: docker logs r112_redis"
  exit 1
fi

# ── 4. 应用 migrations ──
info "应用 PG migrations..."
"$SCRIPT_DIR/local-r112-migrate.sh" || {
  err "migrations 失败, 终止启动"
  exit 1
}

# ── 5. 启动 gateway-v2 (build 首次可能慢) ──
info "启动 gateway-v2 (首次 build 较慢)..."
$COMPOSE_CMD -f "$COMPOSE_FILE" up -d --build gateway-v2

# ── 6. 等待 gateway-v2 /healthz ──
info "等待 gateway-v2 (max 60s)..."
GW_OK=0
for i in $(seq 1 60); do
  if curl -sf http://localhost:8782/healthz >/dev/null 2>&1; then
    GW_OK=1
    ok "gateway-v2 ready (after ${i}s)"
    break
  fi
  sleep 1
done
if [ "$GW_OK" -ne 1 ]; then
  err "gateway-v2 60s 内未就绪"
  err "  修复建议: docker logs r112_gateway_v2"
  err "  或: $COMPOSE_CMD -f $COMPOSE_FILE ps gateway-v2"
  exit 1
fi

# ── 7. 总结 ──
ok "Local R1.12 environment ready"
echo
echo "  - PG:       localhost:5432  (kxuser/kxpass, db=llm_gateway)"
echo "  - Redis:    localhost:6379"
echo "  - LLM mock: http://localhost:1080  (OpenAI 兼容)"
echo "  - Memora:   http://localhost:10302 (mock 占位)"
echo "  - v2 GW:    http://localhost:8782"
echo
echo "下一步:"
echo "  ./scripts/local-r112-smoke.sh          # 跑烟雾测试"
echo "  docker logs -f r112_gateway_v2          # 看 gateway 日志"
echo "  $COMPOSE_CMD -f $COMPOSE_FILE down      # 停止 (保留 volumes)"

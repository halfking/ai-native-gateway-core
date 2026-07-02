#!/usr/bin/env bash
# ─────────────────────────────────────────────────────────────────────
# 本地完整部署: 依赖栈 + 两个 gateway + migrate + smoke
#
# 用法:
#   ./scripts/local-up.sh             # 全链路: 依赖 + v1 + v2 + migrate + smoke
#   ./scripts/local-up.sh --deps      # 只起依赖栈 (PG/Redis/mock)
#   ./scripts/local-up.sh --rebuild   # 强制重建 gateway 镜像后启动
#   ./scripts/local-up.sh --no-smoke  # 启动但不跑 smoke
#   ./scripts/local-up.sh --no-v1     # 不启动 v1 (只起 v2)
#
# 启动后:
#   PG:       localhost:5432  (kxuser/kxpass, db=llm_gateway)
#   Redis:    localhost:6379
#   Mock:     http://localhost:18080  (真 OpenAI 兼容)
#   v1 GW:    http://localhost:8781   (cmd/gateway 生产入口)
#   v2 GW:    http://localhost:8782   (cmd/gateway-v2 演示入口)
# ─────────────────────────────────────────────────────────────────────

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
COMPOSE_FILE="$ROOT_DIR/docker-compose.local-r112.yml"

RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'; NC='\033[0m'
err()  { echo -e "${RED}✗ $*${NC}" >&2; }
ok()   { echo -e "${GREEN}✓ $*${NC}"; }
info() { echo -e "${YELLOW}▶ $*${NC}"; }

# ── 解析参数 ──
DEPS_ONLY=0
REBUILD=0
RUN_SMOKE=1
START_V1=1
for arg in "$@"; do
  case "$arg" in
    --deps)     DEPS_ONLY=1 ;;
    --rebuild)  REBUILD=1 ;;
    --no-smoke) RUN_SMOKE=0 ;;
    --no-v1)    START_V1=0 ;;
    *) err "未知参数: $arg"; echo "用法: $0 [--deps|--rebuild|--no-smoke|--no-v1]"; exit 1 ;;
  esac
done

# ── 选择 compose 命令 ──
if docker compose version >/dev/null 2>&1; then
  COMPOSE_CMD="docker compose"
else
  COMPOSE_CMD="docker-compose"
fi
cd "$ROOT_DIR"

# ── 前置检查 ──
command -v docker >/dev/null 2>&1 || { err "docker 未安装"; exit 1; }
command -v curl >/dev/null 2>&1 || { err "curl 未安装"; exit 1; }

# ── 1. 启动依赖栈 (postgres / redis / mocks) ──
info "启动依赖栈 (postgres, redis, llm-mock, llm-mock-upstream, memora-mcp)..."
$COMPOSE_CMD -f "$COMPOSE_FILE" up -d postgres redis llm-mock llm-mock-upstream memora-mcp

# ── 2. 等待 postgres 健康 ──
info "等待 postgres (max 60s)..."
PG_OK=0
for i in $(seq 1 60); do
  if docker exec r112_postgres pg_isready -U kxuser -d postgres >/dev/null 2>&1; then
    PG_OK=1; ok "postgres ready (after ${i}s)"; break
  fi
  sleep 1
done
[ "$PG_OK" = "1" ] || { err "postgres 60s 内未就绪"; docker logs r112_postgres | tail -20; exit 1; }

# ── 3. 等待 redis + mock-upstream 健康 ──
info "等待 redis + llm-mock-upstream..."
for i in $(seq 1 30); do
  if docker exec r112_redis redis-cli ping 2>/dev/null | grep -q PONG; then
    ok "redis ready"; break
  fi
  sleep 1
done
for i in $(seq 1 30); do
  if curl -sf http://localhost:18080/healthz >/dev/null 2>&1; then
    ok "llm-mock-upstream ready"; break
  fi
  sleep 1
done

# ── 4. 应用 migrations + seed ──
info "应用 PG migrations + local mock credential seed..."
"$SCRIPT_DIR/local-r112-migrate.sh" || { err "migrations 失败"; exit 1; }

# ── 依赖模式到此为止 ──
if [ "$DEPS_ONLY" = "1" ]; then
  ok "依赖栈已就绪 (--deps 模式, 不启动 gateway)"
  echo
  echo "  手动启动 gateway:"
  echo "    v1: $COMPOSE_CMD -f $COMPOSE_FILE up -d gateway"
  echo "    v2: $COMPOSE_CMD -f $COMPOSE_FILE up -d gateway-v2"
  echo "  或直接 go run:"
  echo "    LLM_GATEWAY_DATABASE_URL=postgres://kxuser:kxpass@localhost:5432/llm_gateway?sslmode=disable \\"
  echo "    LLM_GATEWAY_REDIS_ADDR=localhost:6379 LLM_GATEWAY_CREDENTIAL_ENCRYPTION_KEY=AwoRGB8mLTQ7QklQV15lbHN6gYiPlp2kq7K5wMfO1dw \\"
  echo "    go run ./cmd/gateway"
  exit 0
fi

# ── 5. 启动 gateway-v2 ──
BUILD_FLAG=""
[ "$REBUILD" = "1" ] && BUILD_FLAG="--build"
info "启动 gateway-v2${BUILD_FLAG:+ (重建)}..."
$COMPOSE_CMD -f "$COMPOSE_FILE" up -d $BUILD_FLAG gateway-v2

info "等待 gateway-v2 (max 60s)..."
GW_OK=0
for i in $(seq 1 60); do
  if curl -sf http://localhost:8782/healthz >/dev/null 2>&1; then
    GW_OK=1; ok "gateway-v2 ready (after ${i}s)"; break
  fi
  sleep 1
done
[ "$GW_OK" = "1" ] || { err "gateway-v2 未就绪"; docker logs r112_gateway_v2 | tail -30; exit 1; }

# ── 6. 启动 gateway v1 ──
if [ "$START_V1" = "1" ]; then
  info "启动 gateway v1${BUILD_FLAG:+ (重建)}..."
  $COMPOSE_CMD -f "$COMPOSE_FILE" up -d $BUILD_FLAG gateway

  info "等待 gateway v1 (max 90s, 首次 build 较慢)..."
  V1_OK=0
  for i in $(seq 1 90); do
    if curl -sf http://localhost:8781/healthz >/dev/null 2>&1; then
      V1_OK=1; ok "gateway v1 ready (after ${i}s)"; break
    fi
    sleep 1
  done
  if [ "$V1_OK" != "1" ]; then
    err "gateway v1 未就绪 (非致命, v2 测试继续)"
    err "  排查: docker logs r112_gateway | tail -30"
  fi
fi

# ── 7. smoke 测试 ──
if [ "$RUN_SMOKE" = "1" ]; then
  echo
  info "运行 smoke 测试..."
  "$SCRIPT_DIR/local-r112-smoke.sh" || err "smoke 有失败项 (见上方输出)"
fi

# ── 总结 ──
echo
ok "本地环境就绪"
echo
echo "  PG:       localhost:5432  (kxuser/kxpass, db=llm_gateway)"
echo "  Redis:    localhost:6379"
echo "  Mock:     http://localhost:18080  (真 OpenAI 兼容)"
echo "  v1 GW:    http://localhost:8781   (cmd/gateway 生产入口)"
echo "  v2 GW:    http://localhost:8782   (cmd/gateway-v2 演示入口)"
echo
echo "下一步:"
echo "  ./scripts/local-test.sh              # 跑完整测试套件"
echo "  ./scripts/local-r112-smoke.sh        # 只跑 smoke"
echo "  docker logs -f r112_gateway          # v1 日志"
echo "  docker logs -f r112_gateway_v2       # v2 日志"
echo "  ./scripts/local-down.sh              # 停止 (保留 volumes)"

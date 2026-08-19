#!/usr/bin/env bash
# ─────────────────────────────────────────────────────────────────────
# Dev Research 本地集成栈启动 (gateway v1/v2 + 共享 host PG)
#
# 与本地部署 (customer-instance) 不同: dev-research 复用本机 host 上的
# llm-gateway-pg 共享实例, 仅用于研发本地集成测试。
# 客户实例请走: ./scripts/customer-instance-up.sh 或 deploy/one-click/install.sh
#
# 用法:
#   ./scripts/local-up.sh             # 全链路: 依赖 + v1 + v2 + migrate + smoke
#   ./scripts/local-up.sh --deps      # 只起依赖栈 (PG/Redis/mock)
#   ./scripts/local-up.sh --rebuild   # 强制重建 gateway 镜像后启动
#   ./scripts/local-up.sh --no-smoke  # 启动但不跑 smoke
#   ./scripts/local-up.sh --no-v1     # 不启动 v1 (只起 v2)
#
# 前置: cp .env.dev-research.example .env.dev-research 并填入真实凭证 (chmod 0600)
#
# 启动后:
#   PG:       host.docker.internal:5432  (从 .env.dev-research 读密码, db=llm_gateway_test_dev)
#   Redis:    localhost:6379
#   Mock:     http://localhost:${LLM_MOCK_HOST_PORT:-19080}  (真 OpenAI 兼容)
#   v1 GW:    http://localhost:8781   (cmd/gateway 生产入口)
#   v2 GW:    http://localhost:8782   (cmd/gateway-v2 演示入口)
# ─────────────────────────────────────────────────────────────────────

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
COMPOSE_FILE="$ROOT_DIR/docker-compose.dev-research.yml"
ENV_FILE="$ROOT_DIR/.env.dev-research"
ENV_EXAMPLE="$ROOT_DIR/.env.dev-research.example"

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

# ── 凭证检查 (.env.dev-research 必须存在且包含真实密码) ──
if [[ ! -f "$ENV_FILE" ]]; then
  err ".env.dev-research 不存在"
  err "首次使用请: cp $ENV_EXAMPLE $ENV_FILE && chmod 0600 $ENV_FILE && 编辑填入真实凭证"
  exit 1
fi
chmod 0600 "$ENV_FILE" 2>/dev/null || true
# 必填字段校验 (fail-closed, 避免部署时临时生成密码固化在脚本里)
for var in POSTGRES_PASSWORD LLM_GATEWAY_CREDENTIAL_ENCRYPTION_KEY LLM_GATEWAY_JWT_SECRET LLM_GATEWAY_ADMIN_API_KEY LLM_GATEWAY_SEED_ADMIN_PASSWORD; do
  if ! grep -qE "^${var}=.+" "$ENV_FILE" 2>/dev/null; then
    err "$ENV_FILE 缺少必填字段: $var"
    exit 1
  fi
done
ok ".env.dev-research 校验通过 (chmod 0600)"

# ── 1. 启动依赖栈 (redis / mocks; PG 复用 host) ──
info "启动依赖栈 (redis, llm-mock, llm-mock-upstream, memora-mcp; PG 复用 host)..."
$COMPOSE_CMD -f "$COMPOSE_FILE" up -d redis llm-mock llm-mock-upstream memora-mcp

# ── 2. 等待 host PG 健康 (复用本机 host llm-gateway-pg, 不是容器) ──
info "等待 host PG (max 60s)..."
# shellcheck disable=SC1090
set -a; source "$ENV_FILE"; set +a
LLM_MOCK_HOST_PORT="${LLM_MOCK_HOST_PORT:-19080}"
PG_OK=0
for i in $(seq 1 60); do
  if PGPASSWORD="$POSTGRES_PASSWORD" psql -h "$POSTGRES_HOST" -p "$POSTGRES_PORT" -U "$POSTGRES_USER" -d postgres -tAc "SELECT 1" >/dev/null 2>&1; then
    PG_OK=1; ok "host PG ready ($POSTGRES_HOST:$POSTGRES_PORT, after ${i}s)"; break
  fi
  sleep 1
done
[ "$PG_OK" = "1" ] || { err "host PG 60s 内未就绪 (确认 .env.dev-research 中凭证正确 + host llm-gateway-pg 已启动)"; exit 1; }

# ── 3. 等待 redis + mock-upstream 健康 ──
info "等待 redis + llm-mock-upstream..."
for i in $(seq 1 30); do
  if docker exec r112_redis redis-cli ping 2>/dev/null | grep -q PONG; then
    ok "redis ready"; break
  fi
  sleep 1
done
for i in $(seq 1 30); do
  if curl -sf "http://localhost:${LLM_MOCK_HOST_PORT}/healthz" >/dev/null 2>&1; then
    ok "llm-mock-upstream ready"; break
  fi
  sleep 1
done

# ── 4. 应用 migrations + seed (使用 .env.dev-research 中的凭证) ──
info "应用 PG migrations + local mock credential seed..."
export PGPASSWORD="$POSTGRES_PASSWORD"
"$SCRIPT_DIR/local-r112-migrate.sh" || { err "migrations 失败"; exit 1; }

# ── 依赖模式到此为止 ──
if [ "$DEPS_ONLY" = "1" ]; then
  ok "依赖栈已就绪 (--deps 模式, 不启动 gateway)"
  echo
  echo "  手动启动 gateway:"
  echo "    v1: $COMPOSE_CMD -f $COMPOSE_FILE up -d gateway"
  echo "    v2: $COMPOSE_CMD -f $COMPOSE_FILE up -d gateway-v2"
  echo "  或直接 go run (从 .env.dev-research 读凭证):"
  echo "    set -a; source $ENV_FILE; set +a"
  echo "    LLM_GATEWAY_DATABASE_URL=\$LLM_GATEWAY_DATABASE_URL \\"
  echo "      go run ./cmd/gateway"
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
echo "  PG:       \$POSTGRES_HOST:\$POSTGRES_PORT (db=\$POSTGRES_DB, 凭证来自 .env.dev-research)"
echo "  Redis:    localhost:6379"
echo "  Mock:     http://localhost:${LLM_MOCK_HOST_PORT:-19080}  (真 OpenAI 兼容)"
echo "  v1 GW:    http://localhost:8781   (cmd/gateway 生产入口)"
echo "  v2 GW:    http://localhost:8782   (cmd/gateway-v2 演示入口)"
echo
echo "下一步:"
echo "  ./scripts/local-test.sh              # 跑完整测试套件"
echo "  ./scripts/local-r112-smoke.sh        # 只跑 smoke"
echo "  docker logs -f r112_gateway          # v1 日志"
echo "  docker logs -f r112_gateway_v2       # v2 日志"
echo "  ./scripts/local-down.sh              # 停止 (保留 volumes)"

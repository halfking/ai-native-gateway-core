#!/usr/bin/env bash
# ─────────────────────────────────────────────────────────────────────
# Customer-Instance 本地模拟启动 (独立 DB + 部署时临时生成凭证)
#
# 用途: 在本地完整模拟 "客户实例 / 生产部署" 的真实部署体验
#       (与 install.sh /one-click 使用相同的原则: 独立 DB + 临时凭证)
#
# 与 dev-research (local-up.sh) 严格区别:
#   - dev-research: 共享 host PG, 凭证按需覆盖 (.env.dev-research)
#   - customer-instance: 独立 PG 容器, 凭证每次部署时临时生成 (.env.deploy-test)
#
# 用法:
#   ./scripts/customer-instance-up.sh           # 默认: 生成新密码 + 启动栈 + L1-L4
#   ./scripts/customer-instance-up.sh --reuse   # 复用已有 .env.deploy-test (不重新生成)
#   ./scripts/customer-instance-up.sh --clean   # 停 + 清 volumes
#
# 启动后:
#   PG:        localhost:55432  (独立容器 kx-citus-test, 凭证来自 .env.deploy-test)
#   Redis:     localhost:16379  (独立容器 kx-redis-test)
#   Gateway:   localhost:18781
#
# 安全: .env.deploy-test 在仓库 .gitignore 内; chmod 0600
# ─────────────────────────────────────────────────────────────────────

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
COMPOSE_FILE="$ROOT_DIR/docker-compose.deploy-test.yml"
ENV_FILE="$ROOT_DIR/.env.deploy-test"
LOG_FILE="/tmp/customer-instance-up.log"

# 解析参数
REUSE_CREDS=false
CLEAN_ONLY=false
for arg in "$@"; do
  case "$arg" in
    --reuse) REUSE_CREDS=true ;;
    --clean) CLEAN_ONLY=true ;;
    -h|--help)
      sed -n '2,15p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
      exit 0
      ;;
    *) echo "unknown arg: $arg" >&2; exit 1 ;;
  esac
done

# 颜色
if [[ -t 1 ]]; then
  RED=$'\033[0;31m'; GREEN=$'\033[0;32m'; YELLOW=$'\033[0;33m'; CYAN=$'\033[0;36m'; NC=$'\033[0m'
else
  RED=""; GREEN=""; YELLOW=""; CYAN=""; NC=""
fi
log() { echo -e "$*" | tee -a "$LOG_FILE"; }
err() { echo -e "${RED}✗ $*${NC}" | tee -a "$LOG_FILE" >&2; }
ok()  { echo -e "${GREEN}✓ $*${NC}" | tee -a "$LOG_FILE"; }
info(){ echo -e "${YELLOW}▶ $*${NC}" | tee -a "$LOG_FILE"; }
heading() { echo -e "\n${CYAN}━━━ $* ━━━${NC}" | tee -a "$LOG_FILE"; }

# ── 选择 compose 命令 ──
if docker compose version >/dev/null 2>&1; then
  COMPOSE_CMD="docker compose"
else
  COMPOSE_CMD="docker-compose"
fi

# ── 前置检查 ──
heading "前置检查"
command -v docker >/dev/null 2>&1 || { err "docker 未安装"; exit 1; }
command -v openssl >/dev/null 2>&1 || { err "openssl 未安装 (用于生成随机密码)"; exit 1; }
docker info >/dev/null 2>&1 || { err "Docker daemon 未运行"; exit 1; }
ok "docker / openssl / daemon 均就绪"
ok "compose 命令: $COMPOSE_CMD"
[[ -f "$COMPOSE_FILE" ]] || { err "compose 文件不存在: $COMPOSE_FILE"; exit 1; }
ok "compose 文件: $COMPOSE_FILE"

# ── --clean 模式 ──
if [[ "$CLEAN_ONLY" == "true" ]]; then
  heading "清理 customer-instance stack"
  $COMPOSE_CMD -f "$COMPOSE_FILE" down --volumes --remove-orphans 2>&1 | tee -a "$LOG_FILE" || true
  rm -f "$ENV_FILE"
  ok "已停止 + 清 volumes + 删除 .env.deploy-test"
  exit 0
fi

# ── 生成 / 加载凭证 ──
heading "凭证管理 (.env.deploy-test)"

if [[ "$REUSE_CREDS" == "true" && -f "$ENV_FILE" ]]; then
  info "复用已有 $ENV_FILE (--reuse 模式)"
  chmod 0600 "$ENV_FILE"
elif [[ -f "$ENV_FILE" ]]; then
  info "检测到 $ENV_FILE 已存在, 但未指定 --reuse; 删除并重新生成"
  info "(若想复用, 请改用 --reuse)"
  rm -f "$ENV_FILE"
fi

if [[ ! -f "$ENV_FILE" ]]; then
  info "生成新凭证 (openssl rand -hex 24, 48 字符)..."
  POSTGRES_USER="kxuser"
  POSTGRES_PASSWORD="$(openssl rand -hex 24)"
  POSTGRES_PORT="55432"
  POSTGRES_DB="llm_gateway"
  REDIS_PASSWORD="$(openssl rand -hex 24)"
  REDIS_PORT="16379"
  APP_PORT="18781"

  # encryption key (32-byte base64url) — 与 install.sh (customer-instance) 兼容
  LLM_GATEWAY_CREDENTIAL_ENCRYPTION_KEY="$(openssl rand -base64 32 | tr '+/' '-_' | tr -d '=')"
  LLM_GATEWAY_JWT_SECRET="$(openssl rand -hex 32)"
  LLM_GATEWAY_ADMIN_API_KEY="$(openssl rand -hex 32)"
  LLM_GATEWAY_SEED_ADMIN_PASSWORD="$(openssl rand -hex 16)"

  umask 077
  cat > "$ENV_FILE" <<EOF
# Customer-instance 部署凭证 (由 customer-instance-up.sh 生成)
# ─────────────────────────────────────────────────────────────────────
# 生成时间: $(date -u +%Y-%m-%dT%H:%M:%SZ)
# 安全: chmod 0600, .gitignore 忽略, 仅本机使用
# 注意: 重新部署会重新生成; 想保留请用 --reuse 或自行备份
# ─────────────────────────────────────────────────────────────────────

POSTGRES_USER=${POSTGRES_USER}
POSTGRES_PORT=${POSTGRES_PORT}
POSTGRES_PASSWORD=${POSTGRES_PASSWORD}
POSTGRES_DB=${POSTGRES_DB}

REDIS_PASSWORD=${REDIS_PASSWORD}
REDIS_PORT=${REDIS_PORT}

APP_PORT=${APP_PORT}

LLM_GATEWAY_CREDENTIAL_ENCRYPTION_KEY=${LLM_GATEWAY_CREDENTIAL_ENCRYPTION_KEY}
LLM_GATEWAY_JWT_SECRET=${LLM_GATEWAY_JWT_SECRET}
LLM_GATEWAY_ADMIN_API_KEY=${LLM_GATEWAY_ADMIN_API_KEY}
LLM_GATEWAY_SEED_ADMIN_PASSWORD=${LLM_GATEWAY_SEED_ADMIN_PASSWORD}
EOF
  chmod 0600 "$ENV_FILE"
  ok "凭证已写入 $ENV_FILE (chmod 0600)"
  info "PG/Redis/encryption/admin 密码均为本次临时生成 (48 字符 hex / base64url)"
else
  info "复用 $ENV_FILE"
fi

# 加载到当前 shell
# shellcheck disable=SC1090
set -a; source "$ENV_FILE"; set +a

# ── 启动 stack ──
heading "启动 customer-instance stack"
$COMPOSE_CMD -f "$COMPOSE_FILE" --env-file "$ENV_FILE" up -d --build 2>&1 | tee -a "$LOG_FILE"
ok "stack 启动完成"

# ── 等待 PG 就绪 ──
heading "等待 PG 健康"
PG_OK=0
for i in $(seq 1 90); do
  if PGPASSWORD="$POSTGRES_PASSWORD" pg_isready -h localhost -p "$POSTGRES_PORT" -U "$POSTGRES_USER" -d "$POSTGRES_DB" >/dev/null 2>&1; then
    PG_OK=1; ok "PG ready (after ${i}s, port=$POSTGRES_PORT)"; break
  fi
  sleep 1
done
[[ "$PG_OK" == "1" ]] || { err "PG 90s 内未就绪; docker logs kx-citus-test | tail -50"; exit 1; }

# ── 等待 Redis 就绪 ──
heading "等待 Redis 健康"
for i in $(seq 1 30); do
  if docker exec kx-redis-test redis-cli -a "$REDIS_PASSWORD" ping 2>/dev/null | grep -q PONG; then
    ok "Redis ready (after ${i}s)"; break
  fi
  sleep 1
done

# ── 等待 Gateway 就绪 ──
heading "等待 Gateway 就绪"
GW_OK=0
for i in $(seq 1 90); do
  if curl -sf "http://localhost:${APP_PORT}/healthz" >/dev/null 2>&1; then
    GW_OK=1; ok "Gateway ready (after ${i}s, port=$APP_PORT)"; break
  fi
  sleep 1
done
[[ "$GW_OK" == "1" ]] || { err "Gateway 90s 内未就绪; docker logs deploy-test-gateway | tail -50"; exit 1; }

# ── 总结 ──
heading "Customer-instance 部署完成"
ok "凭证: $ENV_FILE (chmod 0600)"
ok "PG:    localhost:$POSTGRES_PORT  (容器 kx-citus-test)"
ok "Redis: localhost:$REDIS_PORT  (容器 kx-redis-test)"
ok "GW:    http://localhost:$APP_PORT"
echo ""
echo "管理命令:"
echo "  查凭证: cat $ENV_FILE"
echo "  PG:     PGPASSWORD=\$POSTGRES_PASSWORD psql -h localhost -p \$POSTGRES_PORT -U \$POSTGRES_USER -d \$POSTGRES_DB"
echo "  Redis:  docker exec -it kx-redis-test redis-cli -a \$REDIS_PASSWORD"
echo "  停:     $0 --clean"
echo "  复用:   $0 --reuse"
echo ""
echo "下次启动会重新生成密码 (除非使用 --reuse)。"
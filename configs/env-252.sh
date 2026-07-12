# ============================================================================
# env-252.sh — 252 Server (Alibaba Cloud) Configuration
#
# Usage: source configs/env-252.sh
# ============================================================================

# ── SSH ────────────────────────────────────────────────────────────────────
SSH_HOST="115.29.212.252"
SSH_PORT="25022"
SSH_USER="root"
# SSH 密码已不再硬编码：必须从 env 注入 DEPLOY_SSH_PASS（推荐使用 SSH 密钥登录）
if [ -z "${DEPLOY_SSH_PASS:-}" ]; then
  echo "必须设置 DEPLOY_SSH_PASS 环境变量" >&2
  return 1 2>/dev/null || exit 1
fi
SSH_PASS="${DEPLOY_SSH_PASS}"

# ── Docker ─────────────────────────────────────────────────────────────────
DOCKER_HOST="${SSH_USER}@${SSH_HOST}"
DOCKER_PG_CONTAINER="pg-252-pg17"

# ── PostgreSQL ─────────────────────────────────────────────────────────────
# Note: 172.16.2.210 is docker-internal to 252, not reachable externally.
# For external access, use SSH tunnel or nginx proxy.
# When using SSH tunnel (local:15432 → 252:172.16.2.210:5432), set:
#   PG_HOST="localhost"
#   PG_PORT="15432"
PG_HOST="localhost"              # Via SSH tunnel (local:15432 → 252:172.16.2.210:5432)
PG_PORT="15432"                 # SSH tunnel port
PG_USER="llm_gateway"
# PG 密码已不再硬编码：必须从 env 注入 PG_PASS（推荐使用 SOPS/Kubernetes Secret）
if [ -z "${PG_PASS:-}" ]; then
  echo "必须设置 PG_PASS 环境变量（DB 密码已不再硬编码）" >&2
  return 1 2>/dev/null || exit 1
fi
PG_PASS="${PG_PASS}"
PG_DB="llm_gateway"

# External access via nginx stream
PG_EXTERNAL_HOST="115.29.212.252"
PG_EXTERNAL_PORT="15432"

# ── SSH Tunnel Config ──────────────────────────────────────────────────────
# Forward local:15432 → 172.16.2.210:5432 via 252
TUNNEL_LOCAL_PORT="15432"
TUNNEL_REMOTE_TARGET="172.16.2.210:5432"

# ── Image Info ─────────────────────────────────────────────────────────────
# 252 server: docker pull kx-citus-pg17:13.3.0 (or platform-specific suffix)
#   - amd64: registry.kxpms.cn/kx-citus-pg17:13.3.0-amd64
#   - arm64: registry.kxpms.cn/kx-citus-pg17:13.3.0-arm64
#   - intel64: registry.kxpms.cn/kx-citus-pg17:13.3.0-intel64
# Local development uses the arm64 variant (Apple Silicon Mac)
PG_IMAGE="registry.kxpms.cn/kx-citus-pg17:13.3.0"
PG_VERSION="17.10 (Debian 17.10-1.pgdg13+1)"
PG_CONTAINER_NAME="pg-252-pg17"

# ── Local PG 17 container (sync target) ───────────────────────────────────
# This is where 252's schema syncs into. NOT on 252 server!
# Bind mount: ~/data/docker/llm-gateway-pg17/data -> /var/lib/postgresql/data
LOCAL_PG_CONTAINER="llm-gateway-pg"
LOCAL_PG_PORT="5432"
LOCAL_PG_USER="llm_gateway"
LOCAL_PG_PASS="llm_gateway_db_pass_2026_secure"
LOCAL_PG_DB="llm_gateway"
LOCAL_PGDATA_BIND="/Users/xutaohuang/data/docker/llm-gateway-pg17/data"

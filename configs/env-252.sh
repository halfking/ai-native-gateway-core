# shellcheck shell=bash disable=SC2034
# ============================================================================
# env-252.sh — 252 PostgreSQL configuration
#
# Usage:
#   source ~/workspace/ai-native-tools/envs/loader.sh --project llm-gateway-go
#   source configs/env-252.sh
#
# PostgreSQL credentials come from the shared envs SSOT. PG_PASS_252 is kept as
# a caller-provided compatibility override; no 252-specific password is stored
# in this repository. SSH uses the local `252` SSH-config alias and key-based
# authentication. The runtime Podman container address is resolved only when a
# tunnel is opened by scripts/lib/252-db-tunnel.sh.
# ============================================================================

# ── SSH ────────────────────────────────────────────────────────────────────
SSH_HOST="115.29.212.252"
SSH_PORT="25022"
SSH_USER="root"
SSH_TARGET="${SSH_TARGET_252:-252}"
# Retained only for consumers that inspect the variable. The supported path is
# SSH config/key authentication, not password authentication.
SSH_PASS="${SSH_PASS_252:-}"

# ── Remote container ───────────────────────────────────────────────────────
REMOTE_PG_CONTAINER="pg-252-pg17"
REMOTE_PG_PORT="5432"
DOCKER_PG_CONTAINER="$REMOTE_PG_CONTAINER"

# ── PostgreSQL ─────────────────────────────────────────────────────────────
# Access is through an SSH tunnel on 127.0.0.1:15432. The remote endpoint is
# intentionally resolved from REMOTE_PG_CONTAINER at invocation time; never
# persist a Podman CNI address in configuration.
PG_HOST="127.0.0.1"
PG_PORT="15432"
PG_USER="llm_gateway"
PG_PASS="${PG_PASS_252:-${COMMON_PG_SUPERUSER_PASS:?COMMON_PG_SUPERUSER_PASS not loaded — source envs/loader.sh --project llm-gateway-go}}"
PG_DB="llm_gateway"
# The project psql shim routes into the local container and cannot reach a host
# tunnel. 252 calls require a native host client; libpq is the supported macOS
# provider after Homebrew PostgreSQL server removal.
PG_PSQL_BIN="${PG_PSQL_BIN:-/opt/homebrew/opt/libpq/bin/psql}"
PG_DUMP_BIN="${PG_DUMP_BIN:-/opt/homebrew/opt/libpq/bin/pg_dump}"

# ── SSH tunnel contract ────────────────────────────────────────────────────
# 15432 is exclusively for the 252 tunnel. Local llm-gateway-pg is published on
# 127.0.0.1:5432. The helper resolves the remote target dynamically.
TUNNEL_LOCAL_PORT="15432"

# ── Image Info ─────────────────────────────────────────────────────────────
PG_IMAGE="registry.kxpms.cn/kx-citus-pg17:13.3.0"
PG_VERSION="17.10 (Debian 17.10-1.pgdg13+1)"
PG_CONTAINER_NAME="$REMOTE_PG_CONTAINER"

# ── Local PG 17 container (sync target) ───────────────────────────────────
# This is local-only metadata. Credentials must come from envs SSOT.
LOCAL_PG_CONTAINER="llm-gateway-pg"
LOCAL_PG_PORT="5432"
LOCAL_PG_USER="llm_gateway"
LOCAL_PG_DB="llm_gateway"
LOCAL_PGDATA_BIND="/Users/xutaohuang/data/docker/llm-gateway-pg17/data"

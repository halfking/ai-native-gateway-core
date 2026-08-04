# ============================================================================
# env-local.sh — Local Development Configuration
# NOTE: TEMPORARILY OVERRIDDEN for sync-from-252.sh (llm-gateway-pg target).
# Original (distribution-bc-pg17) saved in env-local.sh.bak-sync252.
# Before sourcing this file, export PG_PASS_LOCAL through the local secret
# injector (or your shell), for example: export PG_PASS_LOCAL='...'.
# ============================================================================

SSH_HOST="localhost"
SSH_PORT=""
SSH_USER=""
SSH_PASS=""

TARGET_TYPE="docker"
# DOCKER_HOST="local"  # unset: use local docker daemon
DOCKER_PG_CONTAINER="llm-gateway-pg"

PG_HOST="localhost"
PG_PORT="5432"
PG_USER="llm_gateway"
PG_PASS="${PG_PASS_LOCAL:?PG_PASS_LOCAL not set — inject the local PostgreSQL password before sourcing}"
PG_DB="llm_gateway"

PG_IMAGE="kx-citus-pg17:arm64-vector-fixed"
PG_VERSION="17.10 (Debian 17.10-1.pgdg13+1)"

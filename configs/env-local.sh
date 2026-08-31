# ============================================================================
# env-local.sh — Local Development Configuration
# Configured for sync-from-252.sh → target = llm-gateway-pg (kxuser)
# Source after exporting PG_PASS_LOCAL:
#   export PG_PASS_LOCAL='local_kxuser_pw'
#   set -a; . configs/env-local.sh; set +a
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
PG_USER="kxuser"
PG_PASS="${PG_PASS_LOCAL:?PG_PASS_LOCAL not set — export it before sourcing}"
PG_DB="llm_gateway"

PG_IMAGE="kx-citus-pg17:offline-arm64"
PG_VERSION="17.10 (Debian 17.10-1.pgdg13+1)"

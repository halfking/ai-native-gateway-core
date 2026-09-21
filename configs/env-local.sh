# ============================================================================
# env-local.sh — Local Development Configuration
# Configured for sync-from-252.sh → target = llm-gateway-pg
# Source after exporting PG_PASS_LOCAL:
#   export PG_PASS_LOCAL='<superuser pass>'
#   set -a; . configs/env-local.sh; set +a
#
# History: previously set PG_USER="kxuser" but llm-gateway-pg only has the
# superuser role "llm_gateway" (see llm-gateway-pg Docker image / init scripts
# under docker/citus-pg17/). kxuser was either never created or removed during
# the 2026-08-31 port rework. scripts/local-dev/verify-db-data-consistency.sh
# sources this file; with PG_USER=kxuser the docker exec cannot authenticate
# ("FATAL: role 'kxuser' does not exist") and the data audit silently bails
# out at the connectivity check, so an audit run that should fail loudly just
# never starts. Align with the only login role that exists today.
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
PG_PASS="${PG_PASS_LOCAL:?PG_PASS_LOCAL not set — export it before sourcing}"
PG_DB="llm_gateway"

PG_IMAGE="kx-citus-pg17:offline-arm64"
PG_VERSION="17.10 (Debian 17.10-1.pgdg13+1)"

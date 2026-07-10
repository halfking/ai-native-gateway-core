# ============================================================================
# env-local.sh — Local Development Configuration
#
# Usage: source configs/env-local.sh
# ============================================================================

# ── SSH ────────────────────────────────────────────────────────────────────
SSH_HOST="localhost"
SSH_PORT=""                      # Not needed for local
SSH_USER=""                      # Not needed for local
SSH_PASS=""                      # Not needed for local

# ── Docker ─────────────────────────────────────────────────────────────────
DOCKER_HOST="local"
DOCKER_PG_CONTAINER="llm-gateway-pg"

# ── PostgreSQL ─────────────────────────────────────────────────────────────
PG_HOST="localhost"
PG_PORT="5432"
PG_USER="llm_gateway"
PG_PASS="llm_gateway_db_pass_2026_secure"
PG_DB="llm_gateway"

# ── Image Info ─────────────────────────────────────────────────────────────
PG_IMAGE="kx-citus-pg17:arm64"
PG_VERSION="17.10 (Debian 17.10-1.pgdg13+1)"

# ── Note ───────────────────────────────────────────────────────────────────
# After Docker restart, password may need reset:
# docker exec llm-gateway-pg psql -U llm_gateway -d llm_gateway \
#   -c "ALTER USER llm_gateway WITH PASSWORD '${PG_PASS}';"

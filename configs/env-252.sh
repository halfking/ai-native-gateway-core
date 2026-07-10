# ============================================================================
# env-252.sh — 252 Server (Alibaba Cloud) Configuration
#
# Usage: source configs/env-252.sh
# ============================================================================

# ── SSH ────────────────────────────────────────────────────────────────────
SSH_HOST="115.29.212.252"
SSH_PORT="25022"
SSH_USER="root"
SSH_PASS="<SSH_PASSWORD_REDACTED>"

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
PG_PASS="<DB_PASSWORD_REDACTED>"
PG_DB="llm_gateway"

# External access via nginx stream
PG_EXTERNAL_HOST="115.29.212.252"
PG_EXTERNAL_PORT="15432"

# ── SSH Tunnel Config ──────────────────────────────────────────────────────
# Forward local:15432 → 172.16.2.210:5432 via 252
TUNNEL_LOCAL_PORT="15432"
TUNNEL_REMOTE_TARGET="172.16.2.210:5432"

# ── Image Info ─────────────────────────────────────────────────────────────
PG_IMAGE="kx-citus-pg17:amd64"
PG_VERSION="17.10 (Debian 17.10-1.pgdg13+1)"

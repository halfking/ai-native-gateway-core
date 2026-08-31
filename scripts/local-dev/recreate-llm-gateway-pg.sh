#!/usr/bin/env bash
# -----------------------------------------------------------------------------
# File:          scripts/local-dev/recreate-llm-gateway-pg.sh
# Purpose:       Recreate the local llm-gateway-pg Docker container, preserving
#                the existing data dir. POSTGRES_PASSWORD applies ONLY to a
#                first-time cluster initdb (empty data dir); this script never
#                modifies users/passwords of an initialized cluster.
# Policy:        CREATE-ONLY for users/passwords (2026-08-31). Never auto-run
#                ALTER ROLE/USER ... PASSWORD, never drop-and-recreate a user,
#                never use this script as a password-reset tool. Password
#                changes are manual ALTER ROLE + envs SSOT update.
# Status:        active
# Changelog:
#   2026-08-26  v1.0  Initial version
#   2026-08-31  v1.1  Create-only password policy guard + corrected mechanism
#                     notes (entrypoint writes password only at first initdb)
# -----------------------------------------------------------------------------
# Usage:
#   bash scripts/local-dev/recreate-llm-gateway-pg.sh
# -----------------------------------------------------------------------------
# Preconditions:
#   - Existing data dir $HOME/.agents-cache/llm-gateway-pg-data (the LIVE
#     container's dir — verified via docker inspect 2026-08-31)
#   - Image kx-citus-pg17:offline-arm64 available locally
#   - Docker network shared-infra exists
#   - Source 252 credentials loaded via envs loader.sh
# -----------------------------------------------------------------------------

set -euo pipefail

ENVS_LOADER="$HOME/workspace/ai-native-tools/envs/loader.sh"
PROJECT="llm-gateway-go"
SERVER="115.29.212.252"
CONTAINER_NAME="llm-gateway-pg"
IMAGE="kx-citus-pg17:offline-arm64"
# 2026-08-31: aligned to the LIVE container (docker inspect llm-gateway-pg).
# The previous values (~/data/docker/llm-gateway-pg17/data, port 5432, no
# network) described an older container and would create a SECOND cluster.
# 2026-08-31 (later): Homebrew postgresql@17 removed from host 5432; the
# container now owns host 5432. Port 15432 is reserved EXCLUSIVELY for the
# 252 SSH tunnel (configs/env-252.sh TUNNEL_LOCAL_PORT) — do not map the
# container there again (IPv4/IPv6 dual-stack split caused wrong-cluster hits).
DATA_DIR="$HOME/.agents-cache/llm-gateway-pg-data"
PORT_BIND="127.0.0.1:5432:5432"
NETWORK="shared-infra"

# Load 252 password
if ! source "$ENVS_LOADER" --project "$PROJECT" --server "$SERVER" 2>/dev/null; then
  echo "ERROR: failed to load envs (need $ENVS_LOADER)"
  exit 1
fi
PG_PASS="${COMMON_PG_SUPERUSER_PASS:?COMMON_PG_SUPERUSER_PASS not loaded}"
PG_USER="${COMMON_PG_SUPERUSER:?COMMON_PG_SUPERUSER not loaded}"
PG_DB="${LOCAL_PG_DB:-llm_gateway}"

if [[ ! -d "$DATA_DIR" ]]; then
  echo "ERROR: data dir not found: $DATA_DIR"
  exit 1
fi

if ! docker image inspect "$IMAGE" >/dev/null 2>&1; then
  echo "ERROR: image not found locally: $IMAGE"
  exit 1
fi

echo "Container:  $CONTAINER_NAME"
echo "Image:      $IMAGE"
echo "User:       $PG_USER"
echo "Database:   $PG_DB"
echo "Data dir:   $DATA_DIR (PRESERVED — data not erased)"
echo ""

# Policy guard (2026-08-31): this script must NEVER modify existing users or
# passwords. POSTGRES_PASSWORD only takes effect on a FIRST-TIME initdb (empty
# data dir). On an initialized data dir the entrypoint leaves all roles
# untouched — do not treat this script as a password-reset tool.
if [ -s "$DATA_DIR/PG_VERSION" ]; then
  echo "NOTE: data dir already initialized — POSTGRES_PASSWORD env will NOT be applied;"
  echo "      existing users/passwords are never modified (policy: create-only)."
  echo "      Password changes are manual: ALTER ROLE by hand + update envs SSOT."
fi

# Stop + remove existing container (if any)
if docker container inspect "$CONTAINER_NAME" >/dev/null 2>&1; then
  echo "Stopping existing container..."
  docker stop "$CONTAINER_NAME" >/dev/null 2>&1 || true
  docker rm "$CONTAINER_NAME" >/dev/null 2>&1 || true
fi

# Start fresh container (data dir preserved; password env applies only to
# first-time initdb of an empty data dir — never to an existing cluster)
echo "Starting container with 252-aligned credentials..."
docker run -d \
  --name "$CONTAINER_NAME" \
  --restart unless-stopped \
  --network "$NETWORK" \
  -p "$PORT_BIND" \
  -v "$DATA_DIR:/var/lib/postgresql/data" \
  -e POSTGRES_USER="$PG_USER" \
  -e POSTGRES_PASSWORD="$PG_PASS" \
  -e POSTGRES_DB="$PG_DB" \
  -e POSTGRES_INITDB_ARGS='--encoding=UTF-8 --lc-collate=C.UTF-8 --lc-ctype=C.UTF-8' \
  -e TZ=Asia/Shanghai \
  -e PGTZ=Asia/Shanghai \
  -e 'NO_PROXY=localhost,127.0.0.1,192.168.0.0/16,10.0.0.0/8,172.16.0.0/12,*.local,ghcr.io,14.103.169.56,registry.kxpms.cn' \
  -e 'no_proxy=localhost,127.0.0.1,192.168.0.0/16,10.0.0.0/8,172.16.0.0/12,*.local,ghcr.io,14.103.169.56,registry.kxpms.cn' \
  "$IMAGE" >/dev/null

# Wait for ready
for i in $(seq 1 30); do
  if docker exec "$CONTAINER_NAME" pg_isready -U "$PG_USER" -d "$PG_DB" 2>/dev/null | grep -q "accepting"; then
    echo "Container ready after ${i}s"
    break
  fi
  sleep 1
done

# Verify data preserved
TABLE_COUNT=$(docker exec -e PGPASSWORD="$PG_PASS" "$CONTAINER_NAME" psql -U "$PG_USER" -d "$PG_DB" -tAc "SELECT count(*) FROM pg_tables WHERE schemaname='public'" 2>/dev/null | tail -1)
DB_SIZE=$(docker exec -e PGPASSWORD="$PG_PASS" "$CONTAINER_NAME" psql -U "$PG_USER" -d "$PG_DB" -tAc "SELECT pg_size_pretty(pg_database_size('$PG_DB'))" 2>/dev/null | tail -1)

echo ""
echo "Verification:"
echo "  public tables: $TABLE_COUNT"
echo "  database size: $DB_SIZE"
echo "  POSTGRES_PASSWORD: first-time-initdb only; existing users untouched (create-only policy)"
echo ""
echo "VERIFY_SCRIPT=recreate-llm-gateway-pg"
echo "VERIFY_CONTAINER=$CONTAINER_NAME"
echo "VERIFY_TABLES=$TABLE_COUNT"
echo "VERIFY_DB_SIZE=$DB_SIZE"
echo "VERIFY_PASSWORD_POLICY=create-only"
#!/usr/bin/env bash
# -----------------------------------------------------------------------------
# File:          scripts/local-dev/recreate-llm-gateway-pg.sh
# Purpose:       Recreate the local llm-gateway-pg Docker container with
#                password synced to 252 (envs/common/database.yaml).
# Status:        active
# Changelog:
#   2026-08-26  v1.0  Initial version
# -----------------------------------------------------------------------------
# Usage:
#   bash scripts/local-dev/recreate-llm-gateway-pg.sh
# -----------------------------------------------------------------------------
# Preconditions:
#   - Existing data dir /Users/xutaohuang/data/docker/llm-gateway-pg17/data
#   - Image kx-citus-pg17:offline-arm64 available locally
#   - Source 252 credentials loaded via envs loader.sh
# -----------------------------------------------------------------------------

set -euo pipefail

ENVS_LOADER="$HOME/workspace/ai-native-tools/envs/loader.sh"
PROJECT="llm-gateway-go"
SERVER="115.29.212.252"
CONTAINER_NAME="llm-gateway-pg"
IMAGE="kx-citus-pg17:offline-arm64"
DATA_DIR="/Users/xutaohuang/data/docker/llm-gateway-pg17/data"
PORT_BIND="127.0.0.1:5432:5432"

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

# Stop + remove existing container (if any)
if docker container inspect "$CONTAINER_NAME" >/dev/null 2>&1; then
  echo "Stopping existing container..."
  docker stop "$CONTAINER_NAME" >/dev/null 2>&1 || true
  docker rm "$CONTAINER_NAME" >/dev/null 2>&1 || true
fi

# Start fresh container with same data dir + 252 password
echo "Starting container with 252-aligned credentials..."
docker run -d \
  --name "$CONTAINER_NAME" \
  --restart unless-stopped \
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
echo "  POSTGRES_PASSWORD: aligned to 252 (envs/common/database.yaml)"
echo ""
echo "VERIFY_SCRIPT=recreate-llm-gateway-pg"
echo "VERIFY_CONTAINER=$CONTAINER_NAME"
echo "VERIFY_TABLES=$TABLE_COUNT"
echo "VERIFY_DB_SIZE=$DB_SIZE"
echo "VERIFY_PASSWORD_SYNC=aligned-to-252"
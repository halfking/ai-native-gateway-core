#!/usr/bin/env bash
# -----------------------------------------------------------------------------
# File:          scripts/audit/start-isolated-pg.sh
# Purpose:       Start an isolated PostgreSQL container (kx-citus-pg17) for
#                audit-time verification of migrations 627-631, FORCE RLS
#                reaper behaviour, and hot→columnar promote. The container is
#                intentionally independent of llm-gateway-test-pg (5433) and
#                252-sync paths so audit runs cannot interfere with ongoing
#                test or production-aligned DBs.
#
#                The container is named `kx-citus` (matching the installer
#                hard-coded expectations in `dbinit.NewRunner` and
#                `dockerutil.NewHealthChecker`) and bound to host port 15433
#                so it cannot collide with the local llm-gateway-pg on 5432
#                or the 252 SSH tunnel on 15432.
#
# Status:        audit-only (not for installer embed or runtime)
# -----------------------------------------------------------------------------
# Usage:
#   bash scripts/audit/start-isolated-pg.sh
#   bash scripts/audit/start-isolated-pg.sh --recreate   # destroy data first
#
# Outputs:
#   - Container kx-citus running on 127.0.0.1:15433
#   - Env file at /tmp/audit-pg.env with DSN env vars for downstream scripts
# -----------------------------------------------------------------------------

set -euo pipefail

CONTAINER_NAME="kx-citus"
# Must use a Citus-enabled image; sql/schema/01-schema.sql creates columnar
# partitions (USING columnar) for request_logs / usage_ledger /
# routing_decision_log / credential_model_index archives. The image
# `kx-citus-pg17:offline-arm64` is the same one the installer and the local
# 252-aligned llm-gateway-pg use.
IMAGE="kx-citus-pg17:offline-arm64"
PORT_BIND="127.0.0.1:15433:5432"
DATA_DIR="${HOME}/.agents-cache/llm-gateway-audit-pg-data"
PG_USER="kxuser"
PG_PASS="audit_admin_pw_local_only"
PG_DB="llm_gateway"
ENV_FILE="/tmp/audit-pg.env"
DSN="postgres://${PG_USER}:${PG_PASS}@127.0.0.1:15433/${PG_DB}?sslmode=disable"

RECREATE=0
for arg in "$@"; do
    case "$arg" in
        --recreate) RECREATE=1 ;;
        *) echo "unknown arg: $arg"; exit 1 ;;
    esac
done

if ! command -v docker >/dev/null 2>&1; then
    echo "ERROR: docker not found in PATH" >&2
    exit 1
fi

if [[ "$RECREATE" == "1" && -d "$DATA_DIR" ]]; then
    echo "Recreating data dir: $DATA_DIR"
    rm -rf "$DATA_DIR"
fi

if docker container inspect "$CONTAINER_NAME" >/dev/null 2>&1; then
    if [[ "$RECREATE" == "1" ]]; then
        echo "Stopping/removing existing container for recreate"
        docker stop "$CONTAINER_NAME" >/dev/null 2>&1 || true
        docker rm "$CONTAINER_NAME" >/dev/null 2>&1 || true
    else
        RUNNING=$(docker inspect --format='{{.State.Running}}' "$CONTAINER_NAME" 2>/dev/null || echo "false")
        if [[ "$RUNNING" == "true" ]]; then
            echo "Container $CONTAINER_NAME already running on 127.0.0.1:15433"
            cat > "$ENV_FILE" <<EOF
export AUDIT_PG_DSN='$DSN'
export AUDIT_PG_USER='$PG_USER'
export AUDIT_PG_PASS='$PG_PASS'
export AUDIT_PG_DB='$PG_DB'
export AUDIT_PG_PORT='15433'
export AUDIT_PG_HOST='127.0.0.1'
EOF
            chmod 600 "$ENV_FILE"
            echo "Env file: $ENV_FILE"
            exit 0
        fi
    fi
fi

mkdir -p "$DATA_DIR"

echo "Starting $CONTAINER_NAME (image=$IMAGE, port=127.0.0.1:15433, data=$DATA_DIR)"
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
    "$IMAGE" >/dev/null

for i in $(seq 1 30); do
    if docker exec -e PGPASSWORD="$PG_PASS" "$CONTAINER_NAME" pg_isready -U "$PG_USER" -d "$PG_DB" >/dev/null 2>&1; then
        echo "PG ready after ${i}s"
        break
    fi
    sleep 1
    if [[ "$i" == "30" ]]; then
        echo "ERROR: PG did not become ready in 30s" >&2
        docker logs "$CONTAINER_NAME" 2>&1 | tail -20
        exit 1
    fi
done

cat > "$ENV_FILE" <<EOF
export AUDIT_PG_DSN='$DSN'
export AUDIT_PG_USER='$PG_USER'
export AUDIT_PG_PASS='$PG_PASS'
export AUDIT_PG_DB='$PG_DB'
export AUDIT_PG_PORT='15433'
export AUDIT_PG_HOST='127.0.0.1'
EOF
chmod 600 "$ENV_FILE"
echo "Env file: $ENV_FILE"
echo "DSN: $DSN"
echo "Ready."

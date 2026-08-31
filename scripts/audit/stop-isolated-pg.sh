#!/usr/bin/env bash
# -----------------------------------------------------------------------------
# File:          scripts/audit/stop-isolated-pg.sh
# Purpose:       Stop the isolated audit PG container (data preserved).
#                Use --destroy to also remove the data dir.
# -----------------------------------------------------------------------------
set -euo pipefail

CONTAINER_NAME="llm-gateway-audit-pg"
DATA_DIR="${HOME}/.agents-cache/llm-gateway-audit-pg-data"
DESTROY=0
for arg in "$@"; do
    case "$arg" in
        --destroy) DESTROY=1 ;;
        *) echo "unknown arg: $arg"; exit 1 ;;
    esac
done

if ! docker container inspect "$CONTAINER_NAME" >/dev/null 2>&1; then
    echo "Container $CONTAINER_NAME not present; nothing to stop."
    if [[ "$DESTROY" == "1" && -d "$DATA_DIR" ]]; then
        rm -rf "$DATA_DIR"
        echo "Removed data dir: $DATA_DIR"
    fi
    exit 0
fi

docker stop "$CONTAINER_NAME" >/dev/null
docker rm "$CONTAINER_NAME" >/dev/null
echo "Stopped and removed $CONTAINER_NAME"
if [[ "$DESTROY" == "1" ]]; then
    rm -rf "$DATA_DIR"
    echo "Removed data dir: $DATA_DIR"
fi

#!/bin/bash
# tests/local/scripts/wait-healthy.sh
# 等 docker compose services 进入 healthy / running 状态。
#
# 用法: ./wait-healthy.sh postgres redis gateway [...]

set -euo pipefail

TIMEOUT="${TIMEOUT:-60}"
SLEEP="${SLEEP:-2}"
deadline=$(( $(date +%s) + TIMEOUT ))

if [ "$#" -eq 0 ]; then
  echo "Usage: $0 <service-name> [<service-name> ...]" >&2
  exit 2
fi

cd "$(dirname "$0")/.."

while [ "$(date +%s)" -lt "$deadline" ]; do
  ready=1
  for svc in "$@"; do
    state=$(docker compose ps --format "{{.Service}}={{.State}}" "$svc" 2>/dev/null | grep "^${svc}=" | head -1 | cut -d= -f2 || true)
    if [ "$state" != "running" ] && [ "$state" != "healthy" ]; then
      ready=0
      echo "  [$svc] state=$state (waiting)"
      break
    fi
    echo "  [$svc] state=$state ✓"
  done
  if [ "$ready" = "1" ]; then
    echo "All services ready."
    exit 0
  fi
  sleep "$SLEEP"
done

echo "Timed out after ${TIMEOUT}s waiting for: $*" >&2
exit 1

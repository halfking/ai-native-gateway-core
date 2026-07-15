#!/bin/bash
# docs/全方面测试/tools/start_multimodal_supplier.sh
#
# Start 5 multimodal_supplier.py instances on ports 19280-19284.
# Each captures request body structure + fingerprint headers so the
# test client can assert what the gateway forwarded downstream.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]:-$0}")" && pwd)"
LOG_DIR="${LOG_DIR:-/tmp/mm-supplier}"
PID_DIR="${PID_DIR:-/tmp/mm-supplier}"
mkdir -p "$LOG_DIR" "$PID_DIR"

MM_HOST="${MM_HOST:-127.0.0.1}"
MM_BASE_PORT="${MM_BASE_PORT:-19280}"
MM_COUNT="${MM_COUNT:-5}"

# Stop existing
for ((i=0; i<MM_COUNT; i++)); do
  pidfile="$PID_DIR/$i.pid"
  if [ -f "$pidfile" ]; then
    pid=$(cat "$pidfile" 2>/dev/null || true)
    if [ -n "${pid:-}" ] && kill -0 "$pid" 2>/dev/null; then
      kill "$pid" 2>/dev/null || true
    fi
    rm -f "$pidfile"
  fi
done

# Start fresh
for ((i=0; i<MM_COUNT; i++)); do
  port=$((MM_BASE_PORT + i))
  log="$LOG_DIR/$i.log"
  pidfile="$PID_DIR/$i.pid"
  python3 "$SCRIPT_DIR/multimodal_supplier.py" \
    --host "$MM_HOST" --port "$port" --group MM --instance "$i" \
    > "$log" 2>&1 &
  echo $! > "$pidfile"
  sleep 0.2
done

sleep 1
# Health check
healthy=0
for ((i=0; i<MM_COUNT; i++)); do
  port=$((MM_BASE_PORT + i))
  if curl -sf "http://$MM_HOST:$port/healthz" >/dev/null 2>&1; then
    healthy=$((healthy+1))
  fi
done
echo "multimodal suppliers: $healthy/$MM_COUNT healthy on ports $MM_BASE_PORT..$((MM_BASE_PORT+MM_COUNT-1))"

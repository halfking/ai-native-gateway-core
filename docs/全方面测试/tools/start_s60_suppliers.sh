#!/usr/bin/env bash
# 启动 60 个 mock supplier；仅绑定 127.0.0.1，管理接口不对公网暴露。

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
BASE_PORT="${BASE_PORT:-19080}"
PID_DIR="${PID_DIR:-/tmp/llm-gateway-mock-suppliers}"
PYTHON="${PYTHON:-python3}"

mkdir -p "$PID_DIR"

stop_all() {
    if [[ -f "$PID_DIR/pids" ]]; then
        while read -r pid; do
            kill "$pid" 2>/dev/null || true
        done < "$PID_DIR/pids"
        rm -f "$PID_DIR/pids"
    fi
}

if [[ "${1:-}" == "stop" ]]; then
    stop_all
    exit 0
fi

stop_all
: > "$PID_DIR/pids"
for i in $(seq 0 59); do
    port=$((BASE_PORT + i))
    group_idx=$((i / 5))
    instance=$((i % 5))
    group=$(printf "\\$(printf '%o' $((65 + group_idx)))")
    nohup "$PYTHON" "$SCRIPT_DIR/mock_supplier.py" \
        --port "$port" --host 127.0.0.1 --group "$group" --instance "$instance" \
        > "$PID_DIR/${group}${instance}.log" 2>&1 &
    echo $! >> "$PID_DIR/pids"
done

echo "started 60 mock suppliers on 127.0.0.1:${BASE_PORT}-$((BASE_PORT + 59))"

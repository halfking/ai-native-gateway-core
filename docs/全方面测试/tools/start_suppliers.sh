#!/bin/bash
# docs/全方面测试/tools/start_suppliers.sh
#
# 启动 60 个 mock_supplier（A-L × 5 实例，端口 19080-19139）。
# 用法：./start_suppliers.sh [--stop] [--prefix /tmp/lab-suppliers]

set -euo pipefail

# 注意：原变量名 GROUP_NAMES 而非 GROUPS — bash 的 GROUPS 是内建（用户组 IDs）
BASE_PORT=19080
GROUP_NAMES=(A B C D E F G H I J K L)
INSTANCES=(0 1 2 3 4)
PIDS_DIR="${PIDS_DIR:-/tmp/lab-suppliers}"
mkdir -p "$PIDS_DIR"

stop_suppliers() {
    echo "[stop] killing all suppliers..."
    for f in "$PIDS_DIR"/*.pid; do
        [ -e "$f" ] || continue
        pid=$(cat "$f" 2>/dev/null || echo "")
        if [ -n "$pid" ] && kill -0 "$pid" 2>/dev/null; then
            kill "$pid" 2>/dev/null || true
        fi
        rm -f "$f" "$PIDS_DIR/$(basename "$f" .pid).log"
    done
    pkill -f "mock_supplier.py --port" 2>/dev/null || true
    echo "[stop] done"
}

start_suppliers() {
    # 用 BASH_SOURCE 拿绝对路径，避免从 run_all.sh 调用时 $0 是相对路径
    SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]:-$0}")" && pwd)"
    cd "$SCRIPT_DIR"
    echo "[start] launching 60 suppliers (12 groups × 5 instances)..."
    for g in "${GROUP_NAMES[@]}"; do
        for inst in "${INSTANCES[@]}"; do
            # 计算 group 的 base offset
            case "$g" in
                A) base=0 ;;  B) base=5 ;;  C) base=10 ;;  D) base=15 ;;
                E) base=20 ;; F) base=25 ;; G) base=30 ;; H) base=35 ;;
                I) base=40 ;; J) base=45 ;; K) base=50 ;; L) base=55 ;;
            esac
            port=$((BASE_PORT + base + inst))
            LOG="$PIDS_DIR/${g}-${inst}.log"
            PIDF="$PIDS_DIR/${g}-${inst}.pid"
            nohup python3 mock_supplier.py --port "$port" --group "$g" --instance "$inst" \
                > "$LOG" 2>&1 &
            echo $! > "$PIDF"
        done
    done
    sleep 2
    echo "[start] verifying health..."
    cd "$SCRIPT_DIR"
    python3 mock_orchestrator.py health-matrix
    echo "[start] done. logs: $PIDS_DIR/*.log"
}

if [ "${1:-}" = "--stop" ] || [ "${1:-}" = "stop" ]; then
    stop_suppliers
    exit 0
fi
start_suppliers

#!/bin/bash
# S07: 高峰动态调度 — 高并发 + 5 模型 → K 组 (tier=3 备用) 应被启用
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
source "$SCRIPT_DIR/_lib.sh"

reset_all_suppliers
echo "[S07] peak dispatch: 150 clients"
run_loadtest S07_peak_dispatch \
    --n-clients 150 --rps-per-client 5 --duration "${DURATION_HEAVY:-90}" \
    --models tier --prompt long
print_summary S07_peak_dispatch
echo "  期望：K 组收到流量（启用 tier=3 备用）"

#!/bin/bash
# S13: 无可用节点 — 所有供应商故障 → no_candidate
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
source "$SCRIPT_DIR/_lib.sh"

reset_all_suppliers
echo "[S13] no candidate: 全部 12 组 server_error"
for g in A B C D E F G H I J K L; do
    set_group "$g" server_error
done

run_loadtest S13_no_candidate \
    --n-clients 40 --rps-per-client 5 --duration 30 \
    --models tok3 --prompt short
print_summary S13_no_candidate

reset_all_suppliers
echo "  期望：100% 失败 (503)，网关不崩溃"

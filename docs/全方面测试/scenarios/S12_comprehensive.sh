#!/bin/bash
# S12: 全场景综合压测 — 150 client × 15 model × 混合 + 故障注入
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
source "$SCRIPT_DIR/_lib.sh"

reset_all_suppliers
echo "[S12] comprehensive: 150 clients × 15 models × stream + sticky + 故障"
# 先注入部分故障
set_group G slow
set_group J flaky
set_group B server_error

run_loadtest S12_comprehensive \
    --n-clients 150 --rps-per-client 5 --duration 180 \
    --models all --prompt long \
    --stream-ratio 0.3 --sticky-ratio 0.5

# 解除故障看恢复
reset_all_suppliers
run_loadtest S12_post_recovery \
    --n-clients 150 --rps-per-client 5 --duration 30 \
    --models all --prompt long

print_summary S12_comprehensive
print_summary S12_post_recovery

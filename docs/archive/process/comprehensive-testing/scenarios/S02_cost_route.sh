#!/bin/bash
# S02: 成本优化路由 — C/D 组（plan, cost≈0）应得最多流量
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
source "$SCRIPT_DIR/_lib.sh"

reset_all_suppliers
echo "[S02] cost-optimization: 80 clients, 5 models"
run_loadtest S02_cost_route \
    --n-clients 10 --rps-per-client 3 --duration 30 \
    --models tier --prompt short
print_summary S02_cost_route
echo "  期望：C/D 组占 >60% 流量（参考 ADMIN 观察）"

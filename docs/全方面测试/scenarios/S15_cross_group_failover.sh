#!/bin/bash
# S15: 跨组故障迁移 — A + B 主力组全部故障，应自动迁移到 C/D/H/I/L
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
source "$SCRIPT_DIR/_lib.sh"

reset_all_suppliers
echo "[S15] cross-group failover: A=server_error, B=server_error"
set_group A server_error
set_group B server_error

run_loadtest S15_cross_group_failover \
    --n-clients 10 --rps-per-client 3 --duration 30 \
    --models tier --prompt short
print_summary S15_cross_group

reset_all_suppliers
echo "  期望：100% 成功，A/B 组流量 0%"

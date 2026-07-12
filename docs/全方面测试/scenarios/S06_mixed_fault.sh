#!/bin/bash
# S06: 混合故障韧性 — G + J + B + K 四组同时故障，应不影响健康供应商
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
source "$SCRIPT_DIR/_lib.sh"

reset_all_suppliers
echo "[S06] mixed fault: G=slow, J=flaky, B=server_error, K=rate_limited"
set_group G slow
set_group J flaky
set_group B server_error
set_group K rate_limited
run_loadtest S06_mixed_fault \
    --n-clients 100 --rps-per-client 6 --duration 60 \
    --models tok3 --prompt short
print_summary S06_mixed_fault
echo "  期望：>95% 成功，无级联"

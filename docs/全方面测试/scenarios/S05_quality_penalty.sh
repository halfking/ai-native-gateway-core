#!/bin/bash
# S05: 延迟/质量降权 — G (slow) + J (flaky) 应被降权
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
source "$SCRIPT_DIR/_lib.sh"

reset_all_suppliers
echo "[S05] quality penalty: G=slow, J=flaky"
set_group G slow
set_group J flaky
run_loadtest S05_quality_penalty \
    --n-clients 10 --rps-per-client 3 --duration 30 \
    --models tok3 --prompt short
print_summary S05_quality_penalty
echo "  期望：G<5% 流量, J<1% 流量, 健康组 >90%"

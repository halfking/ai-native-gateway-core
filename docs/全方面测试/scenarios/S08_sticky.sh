#!/bin/bash
# S08: Sticky Session — 30 client, 10 session, 80% 复用率
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
source "$SCRIPT_DIR/_lib.sh"

reset_all_suppliers
echo "[S08] sticky session: 30 clients × 10 sessions × 80% reuse"
run_loadtest S08_sticky \
    --n-clients 30 --rps-per-client 6 --duration 60 \
    --models loadtest-mini-alpha --prompt short \
    --sticky-ratio 0.8 --sticky-pool 10
print_summary S08_sticky
echo "  期望：sticky 准确率 >85%, supplier 分布应稳定"

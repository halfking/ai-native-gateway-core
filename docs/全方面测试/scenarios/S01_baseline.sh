#!/bin/bash
# S01: 基准性能 — 理想状态吞吐上限，所有 12 组 healthy
# 期望：success_rate ≥ 99%，p99 < 1.2s
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
source "$SCRIPT_DIR/_lib.sh"

reset_all_suppliers
echo "[S01] baseline 80 clients × 8 rounds × 3 models"
run_loadtest S01_baseline \
    --n-clients 80 --rps-per-client 8 --duration 60 \
    --models core --prompt short
print_summary S01_baseline

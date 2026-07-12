#!/bin/bash
# S04: 配额耗尽与恢复 — C 组 (TokenPlan) 配额降到 2000 tokens/实例
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
source "$SCRIPT_DIR/_lib.sh"

reset_all_suppliers
echo "[S04] quota exhaustion: C group quota = 2000 tokens"
set_group_quota C 2000 1800
run_loadtest S04_quota_failover \
    --n-clients 60 --rps-per-client 10 --duration 60 \
    --models tok3 --prompt short
print_summary S04_quota_failover
echo "  期望：100% 成功 (流量从 C 转到 D/A/B)"

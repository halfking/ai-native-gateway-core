#!/bin/bash
# S16: 充值快速恢复 — 配额耗尽 → 充值 → 2s 内恢复流量
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
source "$SCRIPT_DIR/_lib.sh"

reset_all_suppliers
echo "[S16] quick recovery: C 配额 2000 → 耗尽 → 充值到 500000"

# 耗尽 C
set_group_quota C 2000 1800
echo "  wave 1: 耗尽 C 组配额"
run_loadtest S16_before_recharge \
    --n-clients 10 --rps-per-client 3 --duration 30 \
    --models tok3 --prompt short

# 充值
set_group_quota C 500000 1800
START=$(date +%s.%N)
echo "  充值完成，等待 2s..."

# 立即重测
sleep 2
run_loadtest S16_after_recharge \
    --n-clients 10 --rps-per-client 3 --duration 30 \
    --models tok3 --prompt short
END=$(date +%s.%N)
ELAPSED=$(python3 -c "print(f'{$END - $START:.2f}')")
echo "  wave1→充值→wave2: $ELAPSED s"

print_summary S16_before_recharge
print_summary S16_after_recharge

reset_all_suppliers
echo "  期望：充值后 C 组立即恢复 (≤2s 内收到流量)"

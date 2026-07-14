#!/bin/bash
# S11: 周期性配额恢复 — 60s 短窗口，wave 1 耗尽，wave 2 验证恢复
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
source "$SCRIPT_DIR/_lib.sh"

reset_all_suppliers
echo "[S11] quota recovery: C 短窗口 3000/60s"
set_group_quota C 3000 60
echo "  wave 1: 耗尽 C 组"
run_loadtest S11_quota_recovery \
    --n-clients 40 --rps-per-client 5 --duration 30 \
    --models tok3 --prompt short

echo "  等待 65s 让配额窗口过期..."
sleep 65

echo "  wave 2: 验证 C 组恢复"
run_loadtest S11_quota_recovery \
    --n-clients 40 --rps-per-client 5 --duration 30 \
    --models tok3 --prompt short

print_summary S11_quota_w1
print_summary S11_quota_w2

reset_all_suppliers
echo "  期望：wave 2 中 C 组重新收到请求"

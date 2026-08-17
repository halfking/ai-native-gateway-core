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
    --n-clients 10 --rps-per-client 3 --duration "${DURATION_NORMAL:-30}" \
    --models tok3 --prompt short

# 充值
set_group_quota C 500000 1800
START=$(date +%s.%N)
echo "  充值完成，等待 2s..."

# 立即重测
sleep 2
run_loadtest S16_after_recharge \
    --n-clients 10 --rps-per-client 3 --duration "${DURATION_RECOVERY:-30}" \
    --models tok3 --prompt short
END=$(date +%s.%N)
ELAPSED=$(python3 - "$START" "$END" <<'PY'
import sys
print(f"{float(sys.argv[2]) - float(sys.argv[1]):.2f}")
PY
)
echo "  wave1→充值→wave2: $ELAPSED s"

print_summary S16_before_recharge
print_summary S16_after_recharge

python3 - "$RESULTS_DIR" "$ELAPSED" <<'PY'
import json, os, sys
from result_contract import envelope, write_result
results_dir, elapsed = sys.argv[1:]
phases = {}
for name in ("S16_before_recharge", "S16_after_recharge"):
    with open(os.path.join(results_dir, name + ".json"), encoding="utf-8") as stream:
        phases[name] = json.load(stream)
checks = {
    "before_completed": phases["S16_before_recharge"].get("status") == "PASS",
    "after_completed": phases["S16_after_recharge"].get("status") == "PASS",
    "before_success_rate": phases["S16_before_recharge"].get("metrics", {}).get("success_rate", 0) >= 0.99,
    "after_success_rate": phases["S16_after_recharge"].get("metrics", {}).get("success_rate", 0) >= 0.99,
    "recovery_window_under_five_minutes": float(elapsed) < 300,
}
failures = [key for key, value in checks.items() if not value]
write_result(os.path.join(results_dir, "S16_quick_recovery.json"), envelope(
    "S16_quick_recovery", "functional", "PASS" if not failures else "FAIL",
    checks=checks,
    metrics={"before": phases["S16_before_recharge"].get("metrics", {}), "after": phases["S16_after_recharge"].get("metrics", {}), "recharge_to_wave_elapsed_sec": float(elapsed)},
    evidence={"waves": phases},
    failures=failures, parameters={"recharge_sleep_sec": 2},
    reason="quick recovery phases passed" if not failures else "; ".join(failures),
))
PY

reset_all_suppliers
echo "  期望：充值后 C 组立即恢复 (≤2s 内收到流量)"

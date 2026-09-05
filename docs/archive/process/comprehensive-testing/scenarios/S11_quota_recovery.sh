#!/bin/bash
# S11: 周期性配额恢复 — 60s 短窗口，wave 1 耗尽，wave 2 验证恢复
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
source "$SCRIPT_DIR/_lib.sh"

reset_all_suppliers
echo "[S11] quota recovery: C 短窗口 3000/${QUOTA_WINDOW_SEC:-60}s"
set_group_quota C 3000 "${QUOTA_WINDOW_SEC:-60}"
echo "  wave 1: 耗尽 C 组"
run_loadtest S11_quota_w1 \
    --n-clients 40 --rps-per-client 5 --duration "${DURATION_NORMAL:-30}" \
    --models tok3 --prompt short

echo "  等待 ${QUOTA_WAIT_SEC:-65}s 让配额窗口过期..."
sleep "${QUOTA_WAIT_SEC:-65}"

echo "  wave 2: 验证 C 组恢复"
run_loadtest S11_quota_w2 \
    --n-clients 40 --rps-per-client 5 --duration "${DURATION_NORMAL:-30}" \
    --models tok3 --prompt short

print_summary S11_quota_w1
print_summary S11_quota_w2

python3 - "$RESULTS_DIR" <<'PY'
import json, os, sys
from result_contract import envelope, write_result
results_dir = sys.argv[1]
phases = {}
for name in ("S11_quota_w1", "S11_quota_w2"):
    with open(os.path.join(results_dir, name + ".json"), encoding="utf-8") as stream:
        phases[name] = json.load(stream)
checks = {
    "wave1_completed": phases["S11_quota_w1"].get("status") == "PASS",
    "wave2_completed": phases["S11_quota_w2"].get("status") == "PASS",
    "wave1_success_rate": phases["S11_quota_w1"].get("metrics", {}).get("success_rate", 0) >= 0.99,
    "wave2_success_rate": phases["S11_quota_w2"].get("metrics", {}).get("success_rate", 0) >= 0.99,
}
failures = [key for key, value in checks.items() if not value]
write_result(os.path.join(results_dir, "S11_quota_recovery.json"), envelope(
    "S11_quota_recovery", "functional", "PASS" if not failures else "FAIL",
    checks=checks,
    metrics={"wave1": phases["S11_quota_w1"].get("metrics", {}), "wave2": phases["S11_quota_w2"].get("metrics", {})},
    evidence={"waves": phases, "wait_sec": int(os.environ.get("QUOTA_WAIT_SEC", "65"))},
    failures=failures, parameters={"quota_window_sec": int(os.environ.get("QUOTA_WINDOW_SEC", "60"))},
    reason="quota recovery phases passed" if not failures else "; ".join(failures),
))
PY

reset_all_suppliers
echo "  期望：wave 2 中 C 组重新收到请求"

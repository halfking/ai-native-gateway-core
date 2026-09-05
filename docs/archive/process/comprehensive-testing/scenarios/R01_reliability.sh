#!/bin/bash
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
source "$SCRIPT_DIR/_lib.sh"

SCENARIO="R01_reliability"
DURATION="${R01_DURATION:-180}"
INTERVAL="${R01_PROBE_INTERVAL:-5}"
LOG_FILE="${R01_LOG_FILE:-/tmp/llm-gateway-reliability-${TEST_RUN_ID:-manual}.log}"
reset_all_suppliers

probe_failures=0
probe_count=0
probe_until=$(( $(date +%s) + DURATION ))
while [ "$(date +%s)" -lt "$probe_until" ]; do
    probe_count=$((probe_count + 1))
    if ! curl -fsS --max-time 3 "$GATEWAY/healthz" >>"$LOG_FILE" 2>&1; then
        probe_failures=$((probe_failures + 1))
    fi
    sleep "$INTERVAL"
done &
PROBE_PID=$!

run_loadtest "${SCENARIO}_steady" \
  --category reliability --n-clients "${R01_CLIENTS:-20}" \
  --rps-per-client "${R01_RPS:-2}" --duration "$DURATION" \
  --models core --prompt short --stream-ratio 0.25
LOAD_RC=$?
wait "$PROBE_PID" || true

set_group G server_error || true
run_loadtest "${SCENARIO}_fault" \
  --category reliability --n-clients "${R01_FAULT_CLIENTS:-20}" \
  --rps-per-client "${R01_FAULT_RPS:-2}" --duration "${R01_FAULT_DURATION:-30}" \
  --models core --prompt short
FAULT_RC=$?
reset_all_suppliers

python3 - "$RESULTS_DIR" "$SCENARIO" "$LOAD_RC" "$FAULT_RC" "$probe_count" "$probe_failures" <<'PY'
import json, os, sys
from result_contract import envelope, write_result
results_dir, scenario, load_rc, fault_rc, probe_count, probe_failures = sys.argv[1:]
phases = {}
for name in (f"{scenario}_steady", f"{scenario}_fault"):
    with open(os.path.join(results_dir, name + ".json"), encoding="utf-8") as stream:
        phases[name.rsplit("_", 1)[-1]] = json.load(stream)
checks = {
    "steady_script_completed": int(load_rc) == 0,
    "fault_script_completed": int(fault_rc) == 0,
    "steady_success_rate": phases["steady"].get("metrics", {}).get("success_rate", 0) >= 0.95,
    "fault_recovered": phases["fault"].get("metrics", {}).get("total", 0) > 0,
    "health_probe_available": int(probe_count) > 0 and int(probe_failures) == 0,
}
failures = [name for name, passed in checks.items() if not passed]
write_result(os.path.join(results_dir, scenario + ".json"), envelope(
    scenario, "reliability", "PASS" if not failures else "FAIL",
    checks=checks,
    metrics={"steady": phases["steady"].get("metrics", {}), "fault": phases["fault"].get("metrics", {}), "probe_count": int(probe_count), "probe_failures": int(probe_failures)},
    evidence={"phases": phases, "probe_log": LOG_FILE, "suppliers_reset": True},
    failures=failures,
    parameters={"duration_sec": int(os.environ.get("R01_DURATION", "180")), "fault": "G.server_error"},
    reason="reliability run passed" if not failures else "; ".join(failures),
))
PY

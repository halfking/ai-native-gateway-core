#!/bin/bash
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
source "$SCRIPT_DIR/_lib.sh"

SCENARIO="C01_concurrency"
reset_all_suppliers

run_loadtest "${SCENARIO}_steady" \
  --category concurrency --n-clients "${C01_STEADY_CLIENTS:-50}" \
  --rps-per-client "${C01_STEADY_RPS:-3}" --duration "${C01_DURATION:-30}" \
  --models core --prompt short
run_loadtest "${SCENARIO}_burst" \
  --category concurrency --n-clients "${C01_BURST_CLIENTS:-150}" \
  --rps-per-client "${C01_BURST_RPS:-5}" --duration "${C01_BURST_DURATION:-15}" \
  --models core --prompt short

python3 - "$RESULTS_DIR" "$SCENARIO" <<'PY'
import json, os, sys
from result_contract import envelope, write_result
results_dir, scenario = sys.argv[1:]
phases = []
for name in (f"{scenario}_steady", f"{scenario}_burst"):
    with open(os.path.join(results_dir, name + ".json"), encoding="utf-8") as stream:
        phases.append(json.load(stream))
checks = {
    "steady_completed": phases[0].get("status") == "PASS",
    "burst_completed": phases[1].get("status") == "PASS",
    "steady_success_rate": phases[0].get("metrics", {}).get("success_rate", 0) >= 0.99,
    "burst_success_rate": phases[1].get("metrics", {}).get("success_rate", 0) >= 0.95,
}
failures = [name for name, passed in checks.items() if not passed]
metrics = {
    "steady": phases[0].get("metrics", {}),
    "burst": phases[1].get("metrics", {}),
}
write_result(os.path.join(results_dir, scenario + ".json"), envelope(
    scenario, "concurrency", "PASS" if not failures else "FAIL",
    checks=checks, metrics=metrics, evidence={"phases": phases},
    failures=failures, parameters={"staircase": ["steady", "burst"]},
    reason="all concurrency phases passed" if not failures else "; ".join(failures),
))
PY
reset_all_suppliers

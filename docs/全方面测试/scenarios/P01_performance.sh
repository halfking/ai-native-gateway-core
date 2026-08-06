#!/bin/bash
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
source "$SCRIPT_DIR/_lib.sh"

SCENARIO="P01_performance"
reset_all_suppliers

run_loadtest "${SCENARIO}_nonstream" \
  --category performance --n-clients "${P01_CLIENTS:-40}" \
  --rps-per-client "${P01_RPS:-3}" --duration "${P01_DURATION:-60}" \
  --models core --prompt short --stream-ratio 0
run_loadtest "${SCENARIO}_stream" \
  --category performance --n-clients "${P01_CLIENTS:-40}" \
  --rps-per-client "${P01_RPS:-3}" --duration "${P01_DURATION:-60}" \
  --models core --prompt short --stream-ratio 1

python3 - "$RESULTS_DIR" "$SCENARIO" <<'PY'
import json, os, sys
from result_contract import envelope, write_result
results_dir, scenario = sys.argv[1:]
phase_data = {}
for name in (f"{scenario}_nonstream", f"{scenario}_stream"):
    with open(os.path.join(results_dir, name + ".json"), encoding="utf-8") as stream:
        phase_data[name.rsplit("_", 1)[-1]] = json.load(stream)
nonstream = phase_data["nonstream"]
stream = phase_data["stream"]
non_metrics = nonstream.get("metrics", {})
stream_metrics = stream.get("metrics", {})
checks = {
    "nonstream_completed": nonstream.get("status") == "PASS",
    "stream_completed": stream.get("status") == "PASS",
    "nonstream_success_rate": non_metrics.get("success_rate", 0) >= 0.99,
    "stream_success_rate": stream_metrics.get("success_rate", 0) >= 0.95,
    "stream_sse_completion": stream_metrics.get("stream_completion_rate", 0) >= 0.95,
    "stream_parse_errors": stream_metrics.get("stream_parse_failed", 0) == 0,
}
failures = [name for name, passed in checks.items() if not passed]
write_result(os.path.join(results_dir, scenario + ".json"), envelope(
    scenario, "performance", "PASS" if not failures else "FAIL",
    checks=checks,
    metrics={"nonstream": non_metrics, "stream": stream_metrics},
    evidence={"phases": phase_data, "resource_metrics": "not_collected"},
    failures=failures,
    parameters={"duration_sec": int(os.environ.get("P01_DURATION", "60"))},
    reason="performance baseline passed" if not failures else "; ".join(failures),
))
PY
reset_all_suppliers

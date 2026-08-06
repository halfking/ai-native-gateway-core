#!/bin/bash
# docs/全方面测试/scenarios/run_all.sh
# Strict run-scoped suite runner. A zero exit code alone is not acceptance evidence.

set -euo pipefail
cd "$(dirname "$0")/.."
export PYTHONPATH="$(pwd)/tools${PYTHONPATH:+:$PYTHONPATH}"

SKIP_SCENARIOS=""
FAST=0
ALLOW_SKIPPED=0
SUITE="functional"
GATEWAY="${GATEWAY:-http://localhost:8781}"
RUN_ID="${TEST_RUN_ID:-run-$(date -u +%Y%m%dT%H%M%SZ)-$$}"
while [ $# -gt 0 ]; do
    case "$1" in
        --skip-scenarios) SKIP_SCENARIOS="$2"; shift 2 ;;
        --fast) FAST=1; shift ;;
        --gateway) GATEWAY="$2"; shift 2 ;;
        --suite) SUITE="$2"; shift 2 ;;
        --run-id) RUN_ID="$2"; shift 2 ;;
        --allow-skipped) ALLOW_SKIPPED=1; shift ;;
        *) echo "unknown: $1" >&2; exit 2 ;;
    esac
done

case "$SUITE" in
    functional) SCENARIOS=(S01_baseline S02_cost_route S03_concurrency_diff S04_quota_failover
        S05_quality_penalty S06_mixed_fault S07_peak_dispatch S08_sticky S09_streaming
        S10_long_prompt S11_quota_recovery S12_comprehensive S13_no_candidate
        S14_model_not_found S15_cross_group_failover S16_quick_recovery S17_stream_continuation
        S18_null_handling S19_tenant_isolation S20_auto_title S21_branch_session
        S22_instant_summary S23_long_text_chunked) ;;
    concurrency) SCENARIOS=(C01_concurrency) ;;
    performance) SCENARIOS=(P01_performance) ;;
    reliability) SCENARIOS=(R01_reliability) ;;
    all) SCENARIOS=(S01_baseline S02_cost_route S03_concurrency_diff S04_quota_failover
        S05_quality_penalty S06_mixed_fault S07_peak_dispatch S08_sticky S09_streaming
        S10_long_prompt S11_quota_recovery S12_comprehensive S13_no_candidate
        S14_model_not_found S15_cross_group_failover S16_quick_recovery S17_stream_continuation
        S18_null_handling S19_tenant_isolation S20_auto_title S21_branch_session
        S22_instant_summary S23_long_text_chunked C01_concurrency P01_performance R01_reliability) ;;
    *) echo "invalid --suite: $SUITE" >&2; exit 2 ;;
esac

if [ "$FAST" = "1" ]; then
    export DURATION_NORMAL=15 DURATION_HEAVY=30 DURATION_RECOVERY=15
    export QUOTA_WINDOW_SEC=10 QUOTA_WAIT_SEC=12
fi

export GATEWAY TEST_RUN_ID="$RUN_ID"
RESULTS_DIR="$(pwd)/results/runs/$RUN_ID"
export RESULTS_DIR
mkdir -p "$RESULTS_DIR"
MANIFEST="$RESULTS_DIR/manifest.json"
python3 - "$MANIFEST" "$RUN_ID" "$SUITE" <<'PY'
import json, sys
path, run_id, suite = sys.argv[1:]
json.dump({"schema_version": "1.0", "run_id": run_id, "suite": suite,
           "scenarios": []}, open(path, "w"), indent=2)
PY

SUPPLIERS_STARTED=0
cleanup() {
    rc=$?
    if [ "$SUPPLIERS_STARTED" = "1" ]; then
        bash tools/start_suppliers.sh stop >/dev/null 2>&1 || true
    fi
    exit "$rc"
}
trap cleanup EXIT INT TERM

printf '%s\n' "===================================================================="
printf '%s\n' " LLM Gateway Strict Test Suite"
printf '%s\n' " run_id=$RUN_ID suite=$SUITE gateway=$GATEWAY"
printf '%s\n' " results=$RESULTS_DIR"
printf '%s\n' "===================================================================="

# Environment failures are represented as a result and stop the suite. They are
# not counted as gateway failures, but they still make release acceptance red.
PREFLIGHT="$RESULTS_DIR/TEST_PREFLIGHT.json"
set +e
python3 tools/preflight.py --gateway "$GATEWAY" --output "$PREFLIGHT" --skip-mocks
PREFLIGHT_RC=$?
set -e
if [ "$PREFLIGHT_RC" -ne 0 ]; then
    echo "BLOCKED_ENVIRONMENT: preflight failed; see $PREFLIGHT"
    exit 1
fi

if [ "$SUITE" = "functional" ] || [ "$SUITE" = "concurrency" ] || \
   [ "$SUITE" = "performance" ] || [ "$SUITE" = "reliability" ] || [ "$SUITE" = "all" ]; then
    bash tools/start_suppliers.sh stop >/dev/null 2>&1 || true
    bash tools/start_suppliers.sh
    SUPPLIERS_STARTED=1
    set +e
    python3 tools/preflight.py --gateway "$GATEWAY" --output "$PREFLIGHT"
    MOCK_PREFLIGHT_RC=$?
    set -e
    if [ "$MOCK_PREFLIGHT_RC" -ne 0 ]; then
        echo "BLOCKED_ENVIRONMENT: mock preflight failed; see $PREFLIGHT"
        exit 1
    fi
fi

# Record the declared scenario set before executing it.
python3 - "$MANIFEST" "${SCENARIOS[@]}" <<'PY'
import json, sys
path = sys.argv[1]
data = json.load(open(path))
data["scenarios"] = sys.argv[2:]
json.dump(data, open(path, "w"), indent=2)
PY

for scenario in "${SCENARIOS[@]}"; do
    if [ -n "$SKIP_SCENARIOS" ]; then
        case " $SKIP_SCENARIOS " in
            *" $scenario "*)
                echo "SKIPPED: $scenario (explicitly requested)"
                python3 - "$RESULTS_DIR/${scenario}.json" "$scenario" <<'PY'
import os, sys
from result_contract import envelope, write_result
path, scenario = sys.argv[1:]
write_result(path, envelope(
    scenario,
    "functional",
    "SKIPPED",
    checks={"executed": False},
    evidence={},
    failures=[],
    parameters={"reason": "explicitly skipped"},
    reason="explicitly skipped",
))
PY
                continue
                ;;
        esac
    fi
    script="scenarios/${scenario}.sh"
    echo "▶ $scenario"
    if [ ! -f "$script" ]; then
        echo "  INVALID: missing script $script"
        python3 - "$RESULTS_DIR/${scenario}.json" "$scenario" <<'PY'
import sys
from result_contract import envelope, write_result
path, scenario = sys.argv[1:]
write_result(path, envelope(
    scenario,
    "functional",
    "INVALID",
    checks={"script_exists": False},
    evidence={},
    failures=["missing scenario script"],
    parameters={},
    reason="missing scenario script",
))
PY
        continue
    fi
    set +e
    bash "$script"
    script_rc=$?
    set -e
    result="$RESULTS_DIR/${scenario}.json"
    if [ "$script_rc" -ne 0 ]; then
        echo "  script exit=$script_rc"
        python3 - "$result" "$scenario" "$script_rc" <<'PY'
import json, os, sys
from result_contract import envelope, write_result
path, scenario, rc = sys.argv[1:]
try:
    with open(path, encoding="utf-8") as stream:
        previous = json.load(stream)
except (OSError, json.JSONDecodeError):
    previous = {}
write_result(path, envelope(
    scenario,
    previous.get("category", "functional"),
    "FAIL",
    checks={"script_exit_zero": False},
    metrics=previous.get("metrics", {}),
    evidence={"previous_result": previous, "script_exit": int(rc)},
    failures=[f"scenario script exited with {rc}"],
    parameters=previous.get("parameters", {}),
    reason=f"scenario script exited with {rc}",
))
PY
    fi
    if [ ! -f "$result" ]; then
        python3 - "$result" "$scenario" <<'PY'
import os, sys
from result_contract import envelope, write_result
path, scenario = sys.argv[1:]
write_result(path, envelope(
    scenario,
    "functional",
    "INVALID",
    checks={"result_file_present": False},
    evidence={},
    failures=["missing result file"],
    parameters={},
    reason="missing result file",
))
PY
    fi
    echo "  result=$result"
done

REPORT="$RESULTS_DIR/REPORT.md"
set +e
python3 tools/validation_report.py --results "$RESULTS_DIR" --manifest "$MANIFEST" > "$REPORT"
REPORT_RC=$?
set -e
if [ "$REPORT_RC" -ne 0 ]; then
    echo "strict report status: non-green (rc=$REPORT_RC)"
else
    echo "strict report status: PASS"
fi

# A skipped scenario is incomplete by default. --allow-skipped explicitly
# permits a non-release report, but the runner still returns non-zero when the
# report contains FAIL/INVALID/BLOCKED_ENVIRONMENT.
if [ "$ALLOW_SKIPPED" = "1" ] && [ "$REPORT_RC" -ne 0 ]; then
    set +e
    python3 tools/validation_report.py --results "$RESULTS_DIR" --manifest "$MANIFEST" \
        --allow-skipped > "$REPORT"
    ALLOW_REPORT_RC=$?
    set -e
    if [ "$ALLOW_REPORT_RC" -eq 0 ]; then
        echo "strict report status: PASS_WITH_SKIPPED"
        exit 0
    fi
fi
exit "$REPORT_RC"

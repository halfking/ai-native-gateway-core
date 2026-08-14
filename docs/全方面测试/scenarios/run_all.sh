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
# 2026-08-13 修订: 默认端口 8793 (与 _lib.sh / 02-测试环境部署.md 对齐, 避开 8781 上的其它 gateway 容器).
# 历史: S22/S23 第一轮失败 30/30 0% succ, 根因是默认 GATEWAY=localhost:8781 撞到了别人 7月31日的旧 binary.
GATEWAY="${GATEWAY:-http://127.0.0.1:8793}"
RUN_ID="${TEST_RUN_ID:-run-$(date -u +%Y%m%dT%H%M%SZ)-$$}"
usage() {
    cat <<'EOF'
Usage: bash scenarios/run_all.sh [options]

Options:
  --suite <functional|concurrency|performance|reliability|all>
  --gateway <url> | --gateway=<url>
  --run-id <id>
  --skip-scenarios "<space-separated scenario names>"
  --fast
  --allow-skipped       Produce a non-release PASS only when all other results pass.
  --help
EOF
}
require_value() {
    if [ "$#" -lt 2 ] || [ -z "$2" ]; then
        echo "missing value for $1" >&2
        exit 2
    fi
}
while [ $# -gt 0 ]; do
    case "$1" in
        --skip-scenarios) require_value "$@"; SKIP_SCENARIOS="$2"; shift 2 ;;
        --fast) FAST=1; shift ;;
        --gateway) require_value "$@"; GATEWAY="$2"; shift 2 ;;
        --gateway=*) GATEWAY="${1#--gateway=}"; [ -n "$GATEWAY" ] || { echo "missing value for --gateway" >&2; exit 2; }; shift ;;
        --suite) require_value "$@"; SUITE="$2"; shift 2 ;;
        --run-id) require_value "$@"; RUN_ID="$2"; shift 2 ;;
        --allow-skipped) ALLOW_SKIPPED=1; shift ;;
        --help|-h) usage; exit 0 ;;
        *) echo "unknown option: $1" >&2; usage >&2; exit 2 ;;
    esac
done

case "$SUITE" in
    functional) SCENARIOS=(S01_baseline S02_cost_route S03_concurrency_diff S04_quota_failover
        S05_quality_penalty S06_mixed_fault S07_peak_dispatch S08_sticky S09_streaming
        S10_long_prompt S11_quota_recovery S12_comprehensive S13_no_candidate
        S14_model_not_found S15_cross_group_failover S16_quick_recovery S17_stream_continuation
        S18_null_handling S19_tenant_isolation S20_auto_title S21_branch_session
        S22_instant_summary S23_long_text_chunked
        # 2026-08-14: flash-disconnect 套件 (S24-S29), 详见 docs/changelogs/2026-08-14-flash-disconnect-suite.md
        S24_supplier_flash_disconnect_single S25_supplier_flash_disconnect_cascade
        S26_sticky_session_survives_disconnect S27_streaming_recovery_after_disconnect
        S28_concurrent_flash_isolation S29_post_disconnect_quota_replay) ;;
    concurrency) SCENARIOS=(C01_concurrency) ;;
    performance) SCENARIOS=(P01_performance) ;;
    reliability) SCENARIOS=(R01_reliability) ;;
    all) SCENARIOS=(S01_baseline S02_cost_route S03_concurrency_diff S04_quota_failover
        S05_quality_penalty S06_mixed_fault S07_peak_dispatch S08_sticky S09_streaming
        S10_long_prompt S11_quota_recovery S12_comprehensive S13_no_candidate
        S14_model_not_found S15_cross_group_failover S16_quick_recovery S17_stream_continuation
        S18_null_handling S19_tenant_isolation S20_auto_title S21_branch_session
        S22_instant_summary S23_long_text_chunked
        S24_supplier_flash_disconnect_single S25_supplier_flash_disconnect_cascade
        S26_sticky_session_survives_disconnect S27_streaming_recovery_after_disconnect
        S28_concurrent_flash_isolation S29_post_disconnect_quota_replay
        C01_concurrency P01_performance R01_reliability) ;;
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
write_environment_block() {
    local reason="$1"
    python3 - "$MANIFEST" "$PREFLIGHT" "$reason" <<'PY'
import json, sys
from result_contract import envelope, write_result

manifest_path, path, reason = sys.argv[1:]
try:
    with open(path, encoding="utf-8") as stream:
        probe = json.load(stream)
except (OSError, json.JSONDecodeError):
    probe = {}
with open(manifest_path, "w", encoding="utf-8") as stream:
    json.dump({"schema_version": "1.0", "run_id": probe.get("run_id"), "suite": "environment", "scenarios": ["TEST_PREFLIGHT"]}, stream, indent=2)
write_result(path, envelope(
    "TEST_PREFLIGHT",
    "environment",
    "BLOCKED_ENVIRONMENT",
    checks={"preflight_passed": False},
    metrics={"p99_required": False},
    evidence={"preflight": probe},
    failures=[reason],
    parameters={"gateway": probe.get("gateway", "")},
    reason=reason,
))
PY
    python3 tools/validation_report.py --results "$RESULTS_DIR" --manifest "$MANIFEST" > "$RESULTS_DIR/REPORT.md" || true
}
set +e
python3 tools/preflight.py --gateway "$GATEWAY" --output "$PREFLIGHT" --skip-mocks
PREFLIGHT_RC=$?
set -e
if [ "$PREFLIGHT_RC" -ne 0 ]; then
    echo "BLOCKED_ENVIRONMENT: preflight failed; see $PREFLIGHT"
    write_environment_block "gateway/database preflight failed"
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
        write_environment_block "mock supplier preflight failed"
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

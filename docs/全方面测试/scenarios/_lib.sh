#!/bin/bash
# docs/全方面测试/scenarios/_lib.sh
# 共享 helper — 所有 S01-S16 脚本 source 这文件。

set -euo pipefail

TOOLS_DIR="$(cd "$(dirname "$0")/../tools" && pwd)"
RESULTS_DIR="$(cd "$(dirname "$0")/.." && pwd)/results"
mkdir -p "$RESULTS_DIR"

GATEWAY="${GATEWAY:-http://localhost:8781}"
API_KEYS="${API_KEYS:-sk-loadtest-01-hash-0000000000000000000000000000000000000001,sk-loadtest-02-hash-0000000000000000000000000000000000000002,sk-loadtest-03-hash-0000000000000000000000000000000000000003,sk-loadtest-04-hash-0000000000000000000000000000000000000004}"

# Reset all suppliers to default state
reset_all_suppliers() {
    cd "$TOOLS_DIR"
    python3 mock_orchestrator.py reset-all 2>&1 | tail -2
    cd "$RESULTS_DIR/.."
}

# Run loadtest with given parameters, write JSON to results/SCENARIO_NAME.json
run_loadtest() {
    local scenario="$1"; shift
    local extra_args="$@"
    cd "$TOOLS_DIR"
    echo "  → loadtest.py $extra_args"
    python3 loadtest.py \
        --gateway "$GATEWAY" \
        --api-keys "$API_KEYS" \
        --scenario "$scenario" \
        --output "$RESULTS_DIR/${scenario}.json" \
        $extra_args 2>&1
    cd "$RESULTS_DIR/.."
}

# Pretty print summary line
print_summary() {
    local scenario="$1"
    local json="$RESULTS_DIR/${scenario}.json"
    [ -f "$json" ] || { echo "  [no result]"; return; }
    python3 - <<EOF
import json
d = json.load(open("$json"))
m = d.get("metrics", {})
print(f"  {m.get('total', 0):5d} req | {m.get('success_rate', 0)*100:5.1f}% OK | "
      f"p50={m.get('p50_ms', 0):5.0f}ms p95={m.get('p95_ms', 0):5.0f}ms p99={m.get('p99_ms', 0):5.0f}ms | "
      f"{m.get('throughput_rps', 0):5.1f} rps | fail={m.get('fail_by_status', {})}")
EOF
}

# Set group state (e.g. set_group_state G slow)
set_group() {
    local group="$1" state="$2"
    cd "$TOOLS_DIR"
    python3 mock_orchestrator.py set-group "$group" "$state" 2>&1 | tail -1
    cd "$RESULTS_DIR/.."
}

# Set quota for whole group
set_group_quota() {
    local group="$1" tokens="$2" window_sec="$3"
    cd "$TOOLS_DIR"
    python3 mock_orchestrator.py set-group-quota "$group" "$tokens" "$window_sec" 2>&1 | tail -1
    cd "$RESULTS_DIR/.."
}

# Set all providers manual_disabled in DB (true|false)
set_db_disabled() {
    local state="$1"
    PGPASSWORD="${PGPASSWORD:-}" psql -h "${PGHOST:-localhost}" -p "${PGPORT:-5432}" \
        -U "${PGUSER:-}" -d "${PGDB:-llm_gateway}" -c \
        "UPDATE providers SET manual_disabled = ${state} WHERE id BETWEEN 9010 AND 9069;" 2>&1
}

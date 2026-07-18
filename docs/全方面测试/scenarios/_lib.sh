#!/bin/bash
# docs/全方面测试/scenarios/_lib.sh
# 共享 helper — 所有 S01-S16 脚本 source 这文件。

set -euo pipefail

TOOLS_DIR="$(cd "$(dirname "${BASH_SOURCE[0]:-$0}")/../tools" && pwd)"
RESULTS_DIR="$(cd "$(dirname "${BASH_SOURCE[0]:-$0}")/.." && pwd)/results"
mkdir -p "$RESULTS_DIR"

GATEWAY="${GATEWAY:-http://localhost:8781}"
# raw API keys (gateway 用 HMAC-SHA256 + secret_key 算 hash，去 api_keys 表查)
# 默认使用 seed.sql 注入的 sk-loadtest-01..08 这 8 把 key
API_KEYS="${API_KEYS:-sk-loadtest-01,sk-loadtest-02,sk-loadtest-03,sk-loadtest-04,sk-loadtest-05,sk-loadtest-06,sk-loadtest-07,sk-loadtest-08}"

# Reset all suppliers to default state
reset_all_suppliers() {
    cd "$TOOLS_DIR"
    python3 mock_orchestrator.py reset-all 2>&1 | tail -2
    cd "$RESULTS_DIR/.."
}

# Refresh model_offers.p95_latency_ms from recent request_logs so the
# router's load-aware selection (P2C + LatencyWeight=0.3) actually has
# up-to-date data to work with. Without this, every credential appears
# equally slow (COALESCE p95=9999), which neutralises the latency-aware
# penalty that S05 / S06 / S12 rely on.
#
# This is what production looks like after a few minutes of traffic
# (auto_index_refresher updates the same column every 5 minutes in
# production; we just do it inline for a 30-second test run).
refresh_p95_metrics() {
    (
        export PGHOST="${PGHOST:-localhost}" PGPORT="${PGPORT:-5432}" \
               PGUSER="${PGUSER:-kxuser}" PGPASSWORD="${PGPASSWORD:-kxpass}" \
               PGDB="${PGDB:-llm_gateway}"
        psql -h "$PGHOST" -p "$PGPORT" -U "$PGUSER" -d "$PGDB" -c "
        WITH p95_data AS (
            SELECT credential_id,
                   COALESCE(PERCENTILE_CONT(0.95) WITHIN GROUP (ORDER BY latency_ms)::int, 100)::int AS p95,
                   COUNT(*)::int AS samples
            FROM request_logs_hot
            WHERE ts >= NOW() - INTERVAL '5 minutes'
              AND credential_id BETWEEN 9010 AND 9069
            GROUP BY credential_id
        )
        UPDATE model_offers mo
        SET p95_latency_ms = GREATEST(p95_data.p95, COALESCE(mo.p95_latency_ms, 100))
        FROM p95_data
        WHERE mo.credential_id = p95_data.credential_id
          AND p95_data.samples >= 5
          AND COALESCE(mo.p95_latency_ms, 0) != p95_data.p95;
    " >/dev/null 2>&1 || true
    )
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

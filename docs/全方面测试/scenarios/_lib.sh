#!/bin/bash
# docs/全方面测试/scenarios/_lib.sh
# 共享 helper — 所有 S01-S16 脚本 source 这文件。

set -euo pipefail

TOOLS_DIR="$(cd "$(dirname "${BASH_SOURCE[0]:-$0}")/../tools" && pwd)"
export PYTHONPATH="$TOOLS_DIR${PYTHONPATH:+:$PYTHONPATH}"
RESULTS_DIR="${RESULTS_DIR:-$(cd "$(dirname "${BASH_SOURCE[0]:-$0}")/.." && pwd)/results}"
mkdir -p "$RESULTS_DIR"

# 2026-08-06 修订: 默认 DB 连接参数与 02-测试环境部署.md §3.1 实战配置对齐。
# 之前默认 kxuser/kxpass 与本仓库实际跑的 llm-gateway-pg 容器 (llm_gateway) 不匹配。
# 仍可通过环境变量覆盖 (env-injector / .env / inline)。
: "${PGHOST:=localhost}"
: "${PGPORT:=5432}"
: "${PGUSER:=llm_gateway}"
: "${PGPASSWORD:=llm_gateway_db_pass_2026_secure}"
: "${PGDATABASE:=llm_gateway}"
: "${PGDB:=llm_gateway}"  # backward-compat alias

# 2026-08-13 修订: GATEWAY 默认值改用一个不可能误连其它 gateway 的端口
# (本地 8781/8782 容易撞到 r112_gateway / llm-gateway-dev 容器).
# 之前 `bash S22.sh` 直接执行会用 GATEWAY=localhost:8781,
# 如果本机恰好有别的 llm-gateway-go 在 8781 跑, 测试会静默打到错的 gateway 上
# (这是 S22/S23 第一轮失败 0/30 的根本原因).
#
# 新策略:
#   1. 默认端口改为 8793 (与本仓库本地部署文档 §3.2 默认端口一致, 见 02-测试环境部署.md).
#   2. source 时立即跑一次 /healthz 探活: 如果不通, WARN+提示用户设 GATEWAY= 而不是静默拒绝.
#   3. 允许通过 override: `GATEWAY=http://127.0.0.1:8781 bash S22.sh` 或 run_all.sh --gateway=.
GATEWAY="${GATEWAY:-http://127.0.0.1:8793}"

# 3. 探活与诊断 - 不阻断 (允许离线 dry-run), 但发出清晰警告.
if [ "${LLMGW_LIB_SUPPRESS_PROBE:-0}" != "1" ]; then
    _probe_url="${GATEWAY}/healthz"
    _probe_resp="$(curl -fsS -m 3 -o /dev/null -w '%{http_code}' "${_probe_url}" 2>/dev/null || echo 000)"
    if [ "$_probe_resp" != "200" ]; then
        echo "[_lib.sh] WARN: ${_probe_url} -> ${_probe_resp} (not 200)" >&2
        echo "[_lib.sh] WARN: 检查 GATEWAY env 或 psql 'docker ps | grep gateway' 看谁占了目标端口." >&2
        echo "[_lib.sh] WARN: 当前 GATEWAY=${GATEWAY}" >&2
        echo "[_lib.sh] WARN: 如确认目标已就绪, 设 LLMGW_LIB_SUPPRESS_PROBE=1 屏蔽本警告." >&2
        unset _probe_url _probe_resp
    fi
fi
# raw API keys (gateway 用 HMAC-SHA256 + secret_key 算 hash，去 api_keys 表查)
# 默认使用 seed.sql 注入的 sk-loadtest-01..08 这 8 把 key + 1 把 admin sk-loadtest-admin-01
API_KEYS="${API_KEYS:-sk-loadtest-01,sk-loadtest-02,sk-loadtest-03,sk-loadtest-04,sk-loadtest-05,sk-loadtest-06,sk-loadtest-07,sk-loadtest-08}"
# 注: sk-loadtest-admin-01 不在 API_KEYS 列表 (DB 只有 8 个 client 域 key).
# Admin API 用 LLM_GATEWAY_ADMIN_API_KEY env 静态 token 校验 (admin/auth.go),
# 与 api_keys 表无关. ADMIN_API_KEY 变量给 S20.5 / S22.5 manual 触发保留.
ADMIN_API_KEY="${ADMIN_API_KEY:-sk-test-admin-key-do-not-use-in-prod}"

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

# write_scenario_result SCENARIO CATEGORY STATUS CHECKS_JSON METRICS_JSON EVIDENCE_JSON FAILURES_JSON PARAMETERS_JSON
# Write one strict, atomic result envelope. All JSON arguments must already be valid JSON.
write_scenario_result() {
    local scenario="$1" category="$2" status="$3" checks="$4" metrics="$5"
    local evidence="$6" failures="$7" parameters="$8"
    python3 - "$RESULTS_DIR/${scenario}.json" "$scenario" "$category" "$status" \
        "$checks" "$metrics" "$evidence" "$failures" "$parameters" <<'PY'
import json, os, sys
from result_contract import envelope, write_result

path, scenario, category, status, checks, metrics, evidence, failures, parameters = sys.argv[1:]
result = envelope(
    scenario,
    category,
    status,
    checks=json.loads(checks),
    metrics=json.loads(metrics),
    evidence=json.loads(evidence),
    failures=json.loads(failures),
    parameters=json.loads(parameters),
)
write_result(path, result)
PY
}

# finalize_load_result SCENARIO CATEGORY CHECKS_JSON [FAILURES_JSON]
# Convert a loadtest observation into an acceptance result.
finalize_load_result() {
    local scenario="$1" category="$2" checks="$3" failures="${4:-[]}"
    python3 - "$RESULTS_DIR/${scenario}.json" "$scenario" "$category" "$checks" "$failures" <<'PY'
import json, sys
from result_contract import envelope, write_result
path, scenario, category, checks_raw, failures_raw = sys.argv[1:]
with open(path, encoding="utf-8") as stream:
    observed = json.load(stream)
checks = json.loads(checks_raw)
failures = json.loads(failures_raw)
status = "PASS" if all(checks.values()) and not failures else "FAIL"
result = envelope(
    scenario, category, status,
    checks=checks,
    metrics=observed.get("metrics", {}),
    evidence={"loadtest": observed, "observed_status": observed.get("status")},
    failures=failures,
    parameters=observed.get("parameters", {}),
    reason="all acceptance checks passed" if status == "PASS" else "; ".join(failures),
)
write_result(path, result)
PY
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

# ────────────────────────────────────────────────────────────────────────────
# 2026-08-06: S20-S23 新增 helper
# ────────────────────────────────────────────────────────────────────────────

# psql_exec SQL [extra psql args...]
# Run a SQL query, return the trimmed stdout. Errors (non-zero exit) are silent
# (caller decides whether to fail). Output is a single line.
psql_exec() {
    local sql="$1"; shift
    PGPASSWORD="$PGPASSWORD" psql -h "$PGHOST" -p "$PGPORT" -U "$PGUSER" -d "$PGDB" \
        -tA -c "$sql" "$@" 2>/dev/null
}

# assert_db_row_count "SQL_COUNT_QUERY" EXPECTED [LABEL]
# Counts rows matching the query and compares to EXPECTED. Exits 0 on match,
# 1 on mismatch. Prints PASS/FAIL line.
assert_db_row_count() {
    local sql="$1" expected="$2" label="${3:-assert_db_row_count}"
    local actual
    actual="$(psql_exec "$sql" || echo -1)"
    if [ "$actual" = "$expected" ]; then
        echo "  ✅ $label: $actual rows (expected $expected)"
        return 0
    else
        echo "  ❌ $label: $actual rows (expected $expected)"
        return 1
    fi
}

# assert_db_value_nonempty "SQL_VALUE_QUERY" [LABEL]
# Runs a query that returns a single value; fails if NULL or empty.
assert_db_value_nonempty() {
    local sql="$1" label="${2:-assert_db_value_nonempty}"
    local val
    val="$(psql_exec "$sql" || echo '')"
    if [ -n "$val" ] && [ "$val" != "null" ] && [ "$val" != "" ]; then
        echo "  ✅ $label: '$val'"
        return 0
    else
        echo "  ❌ $label: empty/null"
        return 1
    fi
}

# wait_for_db_value TIMEOUT_SECONDS INTERVAL_SECONDS SQL_QUERY [EXPECTED_VALUE]
# Polls SQL_QUERY every INTERVAL seconds until it returns EXPECTED_VALUE
# (or any non-empty value if EXPECTED_VALUE omitted). Returns 0 on success,
# 1 on timeout. SQL_QUERY must return a single scalar.
wait_for_db_value() {
    local timeout_s="$1" interval_s="$2" sql="$3" expected="${4:-__nonempty__}"
    local deadline=$(( $(date +%s) + timeout_s ))
    while [ "$(date +%s)" -lt "$deadline" ]; do
        local val
        val="$(psql_exec "$sql" || echo '')"
        if [ "$expected" = "__nonempty__" ]; then
            if [ -n "$val" ] && [ "$val" != "null" ] && [ "$val" != "" ]; then
                echo "  ✓ wait_for_db_value: matched after $((timeout_s - (deadline - $(date +%s))))s, value='$val'"
                return 0
            fi
        else
            if [ "$val" = "$expected" ]; then
                echo "  ✓ wait_for_db_value: matched '$expected' after $((timeout_s - (deadline - $(date +%s))))s"
                return 0
            fi
        fi
        sleep "$interval_s"
    done
    echo "  ✗ wait_for_db_value: timeout after ${timeout_s}s, sql='$sql'"
    return 1
}

# wait_for_session_title SESSION_ID TIMEOUT_SECONDS
# Polls session_titles for the given scoped_session_id. Returns 0 when found.
wait_for_session_title() {
    local sid="$1" timeout_s="${2:-30}"
    wait_for_db_value "$timeout_s" 0.5 \
        "SELECT title FROM session_titles WHERE scoped_session_id = '$sid' AND task_id = 'auto' LIMIT 1" \
        "__nonempty__"
}

# wait_for_session_summary SESSION_ID TIMEOUT_SECONDS
# Polls session_summaries for the given session_key.
wait_for_session_summary() {
    local sid="$1" timeout_s="${2:-30}"
    wait_for_db_value "$timeout_s" 1.0 \
        "SELECT summary FROM session_summaries WHERE session_key = '$sid' LIMIT 1" \
        "__nonempty__"
}

# skip_scenario REASON
# Print SKIPPED reason and exit 0 (used for not-yet-implemented features like
# S21 branch session). Always exit 0 so the parent run_all.sh doesn't fail.
skip_scenario() {
    local reason="${1:-feature not implemented yet}"
    echo "  ⏭  SKIPPED: $reason"
    exit 0
}

# run_chat_rounds SESSION_ID ROUNDS [extra args...]
# Convenience wrapper around chat_rounds_client.py.
run_chat_rounds() {
    local sid="$1"; shift
    local rounds="$1"; shift
    local api_key="${1:-$(echo "$API_KEYS" | cut -d, -f1)}"; shift || true
    cd "$TOOLS_DIR"
    python3 chat_rounds_client.py \
        --gateway "$GATEWAY" \
        --api-key "$api_key" \
        --session-id "$sid" \
        --rounds "$rounds" \
        "$@" 2>&1 | tail -3
    cd "$RESULTS_DIR/.."
}

# Call mock_supplier /admin/scripted-response on a specific port.
# set_mock_scripted_response PORT CONTENT [MODEL_OVERRIDE]
set_mock_scripted_response() {
    local port="$1" content="$2" model_override="${3:-}"
    local body
    if [ -n "$model_override" ]; then
        body=$(printf '{"content": %s, "model_override": %s}' \
            "$(python3 -c 'import json,sys; print(json.dumps(sys.argv[1]))' "$content")" \
            "$(python3 -c 'import json,sys; print(json.dumps(sys.argv[1]))' "$model_override")")
    else
        body=$(printf '{"content": %s}' \
            "$(python3 -c 'import json,sys; print(json.dumps(sys.argv[1]))' "$content")")
    fi
    curl -sS -m 3 -X POST -H "Content-Type: application/json" -d "$body" \
        "http://127.0.0.1:$port/admin/scripted-response" 2>&1 | tail -1
}

# reset_mock_scripted_response PORT  (set back to empty echo)
reset_mock_scripted_response() {
    local port="$1"
    curl -sS -m 3 -X POST -H "Content-Type: application/json" -d '{"content": ""}' \
        "http://127.0.0.1:$port/admin/scripted-response" 2>&1 | tail -1
}


# psql_count SQL — return scalar from SELECT count(*)
# 不引双引号 SQL, 避免子 shell 中 INTERVAL '\'' 引号嵌套
psql_count() {
    local sql="$1"
    PGPASSWORD="$PGPASSWORD" psql -h "$PGHOST" -p "$PGPORT" -U "$PGUSER" -d "$PGDB" \
        -tA -c "$sql" 2>/dev/null
}

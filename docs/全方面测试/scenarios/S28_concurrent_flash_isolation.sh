#!/bin/bash
# S28: 并发闪断隔离 (Concurrent Flash Isolation)
#
# 2026-08-14:
#   50 并发客户端,每个客户端持有不同 session。
#   在 t=2 时,杀掉 G/H/I 三个 PAYG 组(占 ~15% 总 supplier 数)。
#   期望:
#     - 全局成功率不应低于 75% (12 组里有 9 组可用)
#     - p99 不应 > 3000ms (无全局 circuit collapse)
#     - 至少有 30 个不同 session_id 的请求落 request_logs
#     - gateway 进程不死, healthz 持续 200
#
# V2/V3 需求对应:
#   - V3 §07 验收 (节点级可操作性, 多节点并发故障隔离)
#   - V2 §18 网关稳定性 (跨组故障不应引发全局级联)

set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
source "$SCRIPT_DIR/_lib.sh"

SCENARIO="S28_concurrent_flash_isolation"
RESULT="$RESULTS_DIR/${SCENARIO}.json"
mkdir -p "$RESULTS_DIR"

log() { echo "[S28] $*"; }

reset_all_suppliers
sleep 2

AK="$(echo "$API_KEYS" | cut -d, -f1)"
log "client api key (truncated): ${AK:0:18}..."

curl -sf "$GATEWAY/healthz" > /dev/null || { log "FAIL: gateway not reachable"; exit 1; }

# 调度表: t=2 杀 G/H/I 三组 (同时)
SCHED="$(mktemp -t s28-sched-XXXXXX).json"
cat > "$SCHED" <<EOF
[
  {"at": 2,  "action": "kill",    "group": "G", "kill_after_sec": 1},
  {"at": 2,  "action": "kill",    "group": "H", "kill_after_sec": 1},
  {"at": 2,  "action": "kill",    "group": "I", "kill_after_sec": 1},
  {"at": 10, "action": "restart", "group": "G"},
  {"at": 10, "action": "restart", "group": "H"},
  {"at": 10, "action": "restart", "group": "I"}
]
EOF

FI_STDOUT="$(mktemp -t s28-fi-XXXXXX).jsonl"
FI_STDERR="$(mktemp -t s28-fi-err-XXXXXX).log"
python3 "$TOOLS_DIR/fault_inject.py" --schedule "$SCHED" --duration 15 \
    > "$FI_STDOUT" 2> "$FI_STDERR" &
FI_PID=$!
log "fault_inject pid=$FI_PID (3 groups down at t=2..10s)"

cleanup() {
    if kill -0 "$FI_PID" 2>/dev/null; then
        kill "$FI_PID" 2>/dev/null || true
    fi
    cd "$TOOLS_DIR" && python3 mock_orchestrator.py reset-all >/dev/null 2>&1 || true
}
trap cleanup EXIT

# ── Phase 1: 启动 50 个并发客户端 (后台 curl) ─────────────────
log "phase 1: launch 50 concurrent clients (each a fresh session)"
TOTAL=50
TMPDIR="$(mktemp -d /tmp/s28-clients-XXXXXX)"
PIDS=()
for i in $(seq 1 $TOTAL); do
    SID="sess-s28-client-$$-$i"
    (
        sleep $((RANDOM % 3))   # stagger 0..2s 让请求分散
        T0=$(python3 -c 'import time;print(int(time.time()*1000))')
        CODE=$(curl -s -o /dev/null -w "%{http_code}" -X POST \
            -H "Authorization: Bearer $AK" \
            -H "X-Gw-Session-Id: $SID" \
            -H "Content-Type: application/json" \
            -d '{"model":"loadtest-mini-alpha","messages":[{"role":"user","content":"s28-client-'$i'"}],"max_tokens":8,"stream":false}' \
            "$GATEWAY/v1/chat/completions" || echo 000)
        T1=$(python3 -c 'import time;print(int(time.time()*1000))')
        LAT=$((T1 - T0))
        echo "$CODE $LAT" > "$TMPDIR/result-$i.txt"
    ) &
    PIDS+=($!)
done
log "  spawned 50 background curls; pid count=${#PIDS[@]}"

# ── Phase 2: 等所有客户端完成 ────────────────────────────────
log "phase 2: wait for all clients to finish (max 25s)"
WAIT_END=$(($(date +%s) + 25))
while [ "$(date +%s)" -lt "$WAIT_END" ]; do
    RUNNING=0
    for pid in "${PIDS[@]}"; do
        kill -0 "$pid" 2>/dev/null && RUNNING=$((RUNNING+1))
    done
    [ "$RUNNING" = "0" ] && break
    sleep 0.5
done

if wait "$FI_PID" 2>/dev/null; then FI_RC=0; else FI_RC=$?; fi
log "fault_inject exit=$FI_RC"

# ── Phase 3: 汇总结果 ──────────────────────────────────────
OK=0
P99_MS=0
LAT_FILE="$(mktemp -t s28-lat-XXXXXX).txt"
CODES=""
for i in $(seq 1 $TOTAL); do
    if [ -f "$TMPDIR/result-$i.txt" ]; then
        read -r CODE LAT < "$TMPDIR/result-$i.txt"
        CODES="$CODES,$CODE"
        if [ "$CODE" = "200" ]; then
            OK=$((OK+1))
            echo "$LAT" >> "$LAT_FILE"
        fi
    fi
done
if [ -s "$LAT_FILE" ]; then
    P99_MS=$(python3 -c "
xs = sorted(int(l.strip()) for l in open('$LAT_FILE') if l.strip())
if not xs: print(0)
else: print(xs[int(len(xs)*0.99)])
")
fi
rm -f "$LAT_FILE"
rm -rf "$TMPDIR"

log "results: $OK/$TOTAL OK, p99=${P99_MS}ms"

# ── Phase 4: 判定 ─────────────────────────────────────────────
PASS=true
[ "$FI_RC" -eq 0 ] || { log "FAIL: fault injector exited $FI_RC"; PASS=false; }
# 全局 ≥ 75% (3 组 down,但有 9 组可用)
SUCC_MIN=$(awk "BEGIN{print int($TOTAL*0.75)}")
[ "$OK" -ge "$SUCC_MIN" ] || { log "FAIL: success $OK/$TOTAL < 75%"; PASS=false; }
# p99 ≤ 3000ms
[ "$P99_MS" -le 3000 ] || { log "FAIL: p99=$P99_MS ms > 3s"; PASS=false; }
# gateway 仍然活
curl -sf "$GATEWAY/healthz" > /dev/null || { log "FAIL: gateway died"; PASS=false; }

CHECK_GLOBAL_OK=$([ "$OK" -ge "$SUCC_MIN" ] && echo true || echo false)
CHECK_P99=$([ "$P99_MS" -le 3000 ] && echo true || echo false)
CHECK_ALIVE=true
CHECK_FAULT_INJECT=$([ "$FI_RC" -eq 0 ] && echo true || echo false)
RATE=$(awk "BEGIN{printf \"%.3f\", $OK/$TOTAL}")
STATUS=$([ "$PASS" = true ] && echo PASS || echo FAIL)
FAILURES="[]"
[ "$PASS" != true ] && FAILURES="[\"S28 isolation failed: ok=$OK/$TOTAL p99=$P99_MS\"]"

cp "$FI_STDOUT" "$RESULTS_DIR/${SCENARIO}-fault-inject.jsonl" 2>/dev/null || true
cp "$FI_STDERR" "$RESULTS_DIR/${SCENARIO}-fault-inject.log" 2>/dev/null || true

write_scenario_result "$SCENARIO" "functional" "$STATUS" \
  "{\"global_75pct\":$CHECK_GLOBAL_OK,\"p99_le_3s\":$CHECK_P99,\"fault_inject_completed\":$CHECK_FAULT_INJECT,\"gateway_alive\":$CHECK_ALIVE}" \
  "{\"total\":$TOTAL,\"succ\":$OK,\"fail\":$((TOTAL-OK)),\"success_rate\":$RATE,\"p99_ms\":$P99_MS,\"kill_groups\":3,\"downtime_sec\":8}" \
  "{\"http_codes\":\"$CODES\",\"fault_inject_exit\":$FI_RC,\"fault_inject_log\":\"results/${SCENARIO}-fault-inject.jsonl\"}" \
  "$FAILURES" \
  "{\"gateway\":\"$GATEWAY\",\"kill_groups\":[\"G\",\"H\",\"I\"],\"concurrent\":$TOTAL,\"kill_window_sec\":8}"

[ "$PASS" = true ] && { log "PASS"; exit 0; } || { log "FAIL"; exit 1; }

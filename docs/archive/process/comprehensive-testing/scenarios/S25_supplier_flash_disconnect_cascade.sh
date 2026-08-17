#!/bin/bash
# S25: 多组错时闪断 (Cascading Flash Disconnect)
#
# 2026-08-14 (rule 11 §6 + rule 37):
#   错时杀掉 G (t=3) → H (t=9) → I (t=15) 三组,每组 kill 持续 6s,然后
#   错时 restart (t=15 G, t=21 H, t=27 I),测量:
#     - 不应出现 "全局断流" (single global circuit collapse)
#     - 客户端不应经历 panic / connection drop
#     - 即使同时 2 组 down,客户端 success_rate ≥ 50% (因为 12 组里 9 组可用)
#     - p99 应在合理范围 (<3000ms) 不出现 30s+ timeout
#
# V2/V3/V3.1 需求对应:
#   - V2 §18 网关稳定性 (多层故障不应触发 panic)
#   - V3.1 §3.3 队列瀑布流 (队列应能消化突发跨组故障)
#   - V3 §07 验收 (节点级可操作性)

set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
source "$SCRIPT_DIR/_lib.sh"

SCENARIO="S25_supplier_flash_disconnect_cascade"
RESULT="$RESULTS_DIR/${SCENARIO}.json"
mkdir -p "$RESULTS_DIR"

log() { echo "[S25] $*"; }

reset_all_suppliers
sleep 2

AK="$(echo "$API_KEYS" | cut -d, -f1)"
log "client api key (truncated): ${AK:0:18}..."

curl -sf "$GATEWAY/healthz" > /dev/null || { log "FAIL: gateway not reachable"; exit 1; }

# 调度表: 错时 kill/restart
SCHED="$(mktemp -t s25-sched-XXXXXX).json"
cat > "$SCHED" <<EOF
[
  {"at": 3,  "action": "kill",    "group": "G", "kill_after_sec": 1},
  {"at": 9,  "action": "kill",    "group": "H", "kill_after_sec": 1},
  {"at": 15, "action": "kill",    "group": "I", "kill_after_sec": 1},
  {"at": 21, "action": "restart", "group": "G"},
  {"at": 27, "action": "restart", "group": "H"},
  {"at": 33, "action": "restart", "group": "I"}
]
EOF
log "schedule: $SCHED"

FI_STDOUT="$(mktemp -t s25-fi-XXXXXX).jsonl"
FI_STDERR="$(mktemp -t s25-fi-err-XXXXXX).log"
python3 "$TOOLS_DIR/fault_inject.py" --schedule "$SCHED" --duration 42 \
    > "$FI_STDOUT" 2> "$FI_STDERR" &
FI_PID=$!
log "fault_inject.py pid=$FI_PID"

cleanup() {
    if kill -0 "$FI_PID" 2>/dev/null; then
        kill "$FI_PID" 2>/dev/null || true
    fi
    cd "$TOOLS_DIR" && python3 mock_orchestrator.py reset-all >/dev/null 2>&1 || true
}
trap cleanup EXIT

# ── Phase 1: baseline (t=0-2s) ─────────────────────────────────
log "phase 1: baseline 5 requests"
BASELINE_OK=0
for i in 1 2 3 4 5; do
    CODE=$(curl -s -o /dev/null -w "%{http_code}" -X POST \
        -H "Authorization: Bearer $AK" -H "Content-Type: application/json" \
        -d '{"model":"loadtest-mini-alpha","messages":[{"role":"user","content":"hi"}],"max_tokens":10,"stream":false}' \
        "$GATEWAY/v1/chat/completions" || echo 000)
    [ "$CODE" = "200" ] && BASELINE_OK=$((BASELINE_OK+1))
    sleep 0.2
done
log "baseline: $BASELINE_OK/5 OK"

# ── Phase 2: 30s cascade window (持续发请求, 跨 6 次故障事件) ──
log "phase 2: 60 requests during cascade (G/H/I stagger-fail t=3..18)"
WINDOW_TOTAL=0
WINDOW_OK=0
WINDOW_5XX=0
WINDOW_OTHER=0
WINDOW_CODES=""
WINDOW_P99_MS=0
WINDOW_START=$(date +%s)
WINDOW_END=$((WINDOW_START + 30))
LATENCY_FILE="$(mktemp -t s25-lat-XXXXXX).txt"
while [ "$(date +%s)" -lt "$WINDOW_END" ]; do
    T0=$(python3 -c 'import time;print(int(time.time()*1000))')
    CODE=$(curl -s -o /dev/null -w "%{http_code}" -X POST \
        -H "Authorization: Bearer $AK" -H "Content-Type: application/json" \
        -d '{"model":"loadtest-mini-alpha","messages":[{"role":"user","content":"cascade"}],"max_tokens":10,"stream":false}' \
        "$GATEWAY/v1/chat/completions" || echo 000)
    T1=$(python3 -c 'import time;print(int(time.time()*1000))')
    LAT=$((T1 - T0))
    WINDOW_TOTAL=$((WINDOW_TOTAL+1))
    case "$CODE" in
        200) WINDOW_OK=$((WINDOW_OK+1)) ;;
        5*)  WINDOW_5XX=$((WINDOW_5XX+1)) ;;
        *)   WINDOW_OTHER=$((WINDOW_OTHER+1)) ;;
    esac
    WINDOW_CODES="$WINDOW_CODES,$CODE"
    [ "$CODE" = "200" ] && echo "$LAT" >> "$LATENCY_FILE" || true
    sleep 0.45
done
if [ -s "$LATENCY_FILE" ]; then
    WINDOW_P99_MS=$(python3 -c "
import sys
xs = sorted(int(l.strip()) for l in open('$LATENCY_FILE') if l.strip())
if not xs: print(0)
else: print(xs[int(len(xs)*0.99)])
")
fi
rm -f "$LATENCY_FILE"
log "cascade window: $WINDOW_OK/$WINDOW_TOTAL OK, 5xx=$WINDOW_5XX, other=$WINDOW_OTHER, p99=${WINDOW_P99_MS}ms"

# ── Phase 3: recovery (等 fault_inject 跑完 restart, 再发 10 个请求) ──
log "phase 3: wait for all groups restored, then 10 recovery requests"
if wait "$FI_PID" 2>/dev/null; then FI_RC=0; else FI_RC=$?; fi
log "fault_inject exit=$FI_RC"
sleep 2
RECOVERY_OK=0
for i in 1 2 3 4 5 6 7 8 9 10; do
    sleep 0.5
    CODE=$(curl -s -o /dev/null -w "%{http_code}" -X POST \
        -H "Authorization: Bearer $AK" -H "Content-Type: application/json" \
        -d '{"model":"loadtest-mini-alpha","messages":[{"role":"user","content":"recovery"}],"max_tokens":10,"stream":false}' \
        "$GATEWAY/v1/chat/completions" || echo 000)
    [ "$CODE" = "200" ] && RECOVERY_OK=$((RECOVERY_OK+1))
done
log "recovery: $RECOVERY_OK/10 OK"

# ── Phase 4: 判定 ─────────────────────────────────────────────
PASS=true
[ "$FI_RC" -eq 0 ] || { log "FAIL: fault injector exited $FI_RC"; PASS=false; }
[ "$BASELINE_OK" -ge 4 ] || { log "FAIL: baseline $BASELINE_OK/5"; PASS=false; }
WINDOW_OK_MIN=$(awk "BEGIN{print int($WINDOW_TOTAL*0.5)}")
[ "$WINDOW_OK" -ge "$WINDOW_OK_MIN" ] || { log "FAIL: cascade window $WINDOW_OK/$WINDOW_TOTAL < 50%"; PASS=false; }
[ "$RECOVERY_OK" -ge 8 ] || { log "FAIL: recovery $RECOVERY_OK/10"; PASS=false; }
# p99 sanity: 即使有 retry, 也不应超过 3s
[ "$WINDOW_P99_MS" -le 3000 ] || { log "FAIL: p99=$WINDOW_P99_MS ms > 3s"; PASS=false; }
curl -sf "$GATEWAY/healthz" > /dev/null || { log "FAIL: gateway died"; PASS=false; }

CHECK_BASELINE=$([ "$BASELINE_OK" -ge 4 ] && echo true || echo false)
CHECK_WINDOW=$([ "$WINDOW_OK" -ge "$WINDOW_OK_MIN" ] && echo true || echo false)
CHECK_RECOVERY=$([ "$RECOVERY_OK" -ge 8 ] && echo true || echo false)
CHECK_ALIVE=true
CHECK_FAULT_INJECT=$([ "$FI_RC" -eq 0 ] && echo true || echo false)
TOTAL=$((5 + WINDOW_TOTAL + 10))
SUCC=$((BASELINE_OK + WINDOW_OK + RECOVERY_OK))
RATE=$(awk "BEGIN{printf \"%.3f\", $SUCC/$TOTAL}")
STATUS=$([ "$PASS" = true ] && echo PASS || echo FAIL)
FAILURES="[]"
[ "$PASS" != true ] && FAILURES="[\"S25 cascade acceptance failed: b=$BASELINE_OK/5 w=$WINDOW_OK/$WINDOW_TOTAL r=$RECOVERY_OK/10\"]"

cp "$FI_STDOUT" "$RESULTS_DIR/${SCENARIO}-fault-inject.jsonl" 2>/dev/null || true
cp "$FI_STDERR" "$RESULTS_DIR/${SCENARIO}-fault-inject.log" 2>/dev/null || true

write_scenario_result "$SCENARIO" "functional" "$STATUS" \
  "{\"baseline_5_of_5\":$CHECK_BASELINE,\"cascade_50pct\":$CHECK_WINDOW,\"recovery_8_of_10\":$CHECK_RECOVERY,\"p99_le_3s\":$([ "$WINDOW_P99_MS" -le 3000 ] && echo true || echo false),\"fault_inject_completed\":$CHECK_FAULT_INJECT,\"gateway_alive\":$CHECK_ALIVE}" \
  "{\"total\":$TOTAL,\"succ\":$SUCC,\"fail\":$((TOTAL-SUCC)),\"success_rate\":$RATE,\"p99_ms\":$WINDOW_P99_MS,\"baseline_ok\":$BASELINE_OK,\"cascade_ok\":$WINDOW_OK,\"cascade_total\":$WINDOW_TOTAL,\"cascade_5xx\":$WINDOW_5XX,\"cascade_other\":$WINDOW_OTHER,\"cascade_p99_ms\":$WINDOW_P99_MS,\"recovery_ok\":$RECOVERY_OK}" \
  "{\"cascade_codes\":\"$WINDOW_CODES\",\"fault_inject_exit\":$FI_RC,\"fault_inject_log\":\"results/${SCENARIO}-fault-inject.jsonl\"}" \
  "$FAILURES" \
  "{\"gateway\":\"$GATEWAY\",\"kill_groups\":[\"G\",\"H\",\"I\"],\"kill_after_sec\":1,\"window_sec\":30,\"cascade_event_count\":3}"

[ "$PASS" = true ] && { log "PASS"; exit 0; } || { log "FAIL"; exit 1; }

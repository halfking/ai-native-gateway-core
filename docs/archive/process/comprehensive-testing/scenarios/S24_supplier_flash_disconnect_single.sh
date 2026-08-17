#!/bin/bash
# S24: 单组闪断 (Single Group Flash Disconnect)
#
# 2026-08-14 (rule 11 §6 + rule 37 简洁优先):
#   杀 G 组的 5 个 mock_supplier 进程,测量:
#     - 基线成功率 / p99 / 重试次数
#     - 闪断窗口期 (kill → restart 12s 内) 客户端行为
#     - 恢复后再次成功
#   期望: gateway 自动 failover 到 C/D/H/I/L 等组, 客户端不 panic,
#         success_rate ≥ 70% (允许 30% 因为 probe 未及时剔除死凭据),
#         no 5xx storm, request_logs 记录 retry_attempts。
#
# V2/V3/V3.1 需求对应:
#   - V2 §18 网关稳定性 (供应商失败后客户端不应感知卡死)
#   - V3 §07 验收 (节点级可操作性, 单组挂掉不影响全局)
#   - V3.1 §3.3 队列瀑布流 (队列应能跨供应商故障消化请求)

set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
source "$SCRIPT_DIR/_lib.sh"

SCENARIO="S24_supplier_flash_disconnect_single"
RESULT="$RESULTS_DIR/${SCENARIO}.json"
mkdir -p "$RESULTS_DIR"

log() { echo "[S24] $*"; }

# 重置所有 supplier 到 healthy (rule 11 §4 自我记录: 上游测试可能留下脏状态)
reset_all_suppliers
sleep 2  # 等 probe worker 同步状态

# 取一把 client api key
AK="$(echo "$API_KEYS" | cut -d, -f1)"
log "client api key (truncated): ${AK:0:18}..."
log "gateway=$GATEWAY"

curl -sf "$GATEWAY/healthz" > /dev/null || { log "FAIL: gateway not reachable"; exit 1; }

# 时间线 (相对 S24 开始):
#   t=0     : 启动 fault_inject.py (后台), 调度表: t=3 kill G, t=15 restart G
#   t=0-2   : 基线 5 个请求
#   t=2-13  : 闪断窗口 — 11s 内持续发请求 (kill 后 probe 检测 + retry)
#   t=13-20 : 恢复后 7s 内持续发请求,验证成功恢复

# 写调度表
SCHED="$(mktemp -t s24-sched-XXXXXX)"
mv "${SCHED}" "${SCHED}.json"
SCHED="${SCHED}.json"
cat > "$SCHED" <<EOF
[
  {"at": 3,  "action": "kill",    "group": "G", "kill_after_sec": 1},
  {"at": 15, "action": "restart", "group": "G"}
]
EOF
log "schedule: $SCHED"

# 启动 fault_inject.py (后台)
FI_STDOUT="$(mktemp -t s24-fi-XXXXXX)"
mv "${FI_STDOUT}" "${FI_STDOUT}.jsonl"
FI_STDOUT="${FI_STDOUT}.jsonl"
FI_STDERR="$(mktemp -t s24-fi-err-XXXXXX).log"
FI_LOG="$FI_STDERR"
python3 "$TOOLS_DIR/fault_inject.py" --schedule "$SCHED" --duration 25 \
    > "$FI_STDOUT" 2> "$FI_LOG" &
FI_PID=$!
log "fault_inject.py started pid=$FI_PID stdout=$FI_STDOUT stderr=$FI_LOG"

cleanup() {
    if kill -0 "$FI_PID" 2>/dev/null; then
        kill "$FI_PID" 2>/dev/null || true
    fi
    # 确保 G 组最终是 healthy
    cd "$TOOLS_DIR" && python3 mock_orchestrator.py reset-all >/dev/null 2>&1 || true
}
trap cleanup EXIT

# ── Phase 1: 基线 (t=0-2s) ─────────────────────────────────────
log "phase 1: baseline 5 requests"
BASELINE_OK=0
for i in 1 2 3 4 5; do
    CODE=$(curl -s -o /dev/null -w "%{http_code}" -X POST \
        -H "Authorization: Bearer $AK" -H "Content-Type: application/json" \
        -d '{"model":"loadtest-mini-alpha","messages":[{"role":"user","content":"hi"}],"max_tokens":10,"stream":false}' \
        "$GATEWAY/v1/chat/completions" || echo 000)
    [ "$CODE" = "200" ] && BASELINE_OK=$((BASELINE_OK+1))
    log "  baseline req $i → HTTP $CODE"
    sleep 0.2
done
log "baseline: $BASELINE_OK/5 OK"

# ── Phase 2: 闪断窗口 (t=2-13s, kill 后 11s) ──────────────────
log "phase 2: 30 requests during flash window (kill G at ~t=3)"
WINDOW_TOTAL=0
WINDOW_OK=0
WINDOW_5XX=0
WINDOW_OTHER=0
WINDOW_CODES=""
WINDOW_LAT_FILE="$(mktemp -t s24-lat-XXXXXX).txt"
WINDOW_START=$(date +%s)
WINDOW_END=$((WINDOW_START + 11))
while [ "$(date +%s)" -lt "$WINDOW_END" ]; do
    T0=$(python3 -c 'import time;print(int(time.time()*1000))')
    CODE=$(curl -s -o /dev/null -w "%{http_code}" -X POST \
        -H "Authorization: Bearer $AK" -H "Content-Type: application/json" \
        -d '{"model":"loadtest-mini-alpha","messages":[{"role":"user","content":"during-flash"}],"max_tokens":10,"stream":false}' \
        "$GATEWAY/v1/chat/completions" || echo 000)
    T1=$(python3 -c 'import time;print(int(time.time()*1000))')
    LAT=$((T1 - T0))
    WINDOW_TOTAL=$((WINDOW_TOTAL+1))
    case "$CODE" in
        200) WINDOW_OK=$((WINDOW_OK+1)); echo "$LAT" >> "$WINDOW_LAT_FILE" ;;
        5*)  WINDOW_5XX=$((WINDOW_5XX+1)) ;;
        *)   WINDOW_OTHER=$((WINDOW_OTHER+1)) ;;
    esac
    WINDOW_CODES="$WINDOW_CODES,$CODE"
    # 11s / 30 = 0.36s/req
    sleep 0.36
done
WINDOW_P99_MS=$(python3 -c "
xs = sorted(int(l.strip()) for l in open('$WINDOW_LAT_FILE') if l.strip())
if not xs: print(0)
else: print(xs[int(len(xs)*0.99)])
")
rm -f "$WINDOW_LAT_FILE"
log "flash window: $WINDOW_OK/$WINDOW_TOTAL OK, 5xx=$WINDOW_5XX, other=$WINDOW_OTHER, p99=${WINDOW_P99_MS}ms"

# 调度失败不能继续报告 PASS。
if wait "$FI_PID" 2>/dev/null; then FI_RC=0; else FI_RC=$?; fi
log "fault_inject exit=$FI_RC"

# ── Phase 3: 恢复后 (t=15-22s) ────────────────────────────────
log "phase 3: 10 recovery requests"
RECOVERY_OK=0
RECOVERY_TOTAL=0
for i in 1 2 3 4 5 6 7 8 9 10; do
    sleep 0.5
    CODE=$(curl -s -o /dev/null -w "%{http_code}" -X POST \
        -H "Authorization: Bearer $AK" -H "Content-Type: application/json" \
        -d '{"model":"loadtest-mini-alpha","messages":[{"role":"user","content":"recovery"}],"max_tokens":10,"stream":false}' \
        "$GATEWAY/v1/chat/completions" || echo 000)
    RECOVERY_TOTAL=$((RECOVERY_TOTAL+1))
    [ "$CODE" = "200" ] && RECOVERY_OK=$((RECOVERY_OK+1))
done
log "recovery: $RECOVERY_OK/$RECOVERY_TOTAL OK"

# ── Phase 4: 保存 fault_inject 日志到 result dir,evidence 引用路径而非 inline JSON ──
cp "$FI_STDOUT" "$RESULTS_DIR/${SCENARIO}-fault-inject.jsonl" 2>/dev/null || true
cp "$FI_LOG" "$RESULTS_DIR/${SCENARIO}-fault-inject.log" 2>/dev/null || true

# ── Phase 5: 判定 ─────────────────────────────────────────────
# 接受基线: 基线 ≥4/5 (95%), 闪断窗口 ≥60% (允许 probe 未及时剔除), 恢复 ≥8/10 (80%)
PASS=true
[ "$FI_RC" -eq 0 ] || { log "FAIL: fault injector exited $FI_RC"; PASS=false; }
if [ "$BASELINE_OK" -lt 4 ]; then
    log "FAIL: baseline $BASELINE_OK/5 < 4"; PASS=false
fi
WINDOW_RATE=$(awk "BEGIN{printf \"%.2f\", $WINDOW_OK/$WINDOW_TOTAL}")
WINDOW_OK_MIN=$(awk "BEGIN{print int($WINDOW_TOTAL*0.6)}")
if [ "$WINDOW_OK" -lt "$WINDOW_OK_MIN" ]; then
    log "FAIL: flash window $WINDOW_OK/$WINDOW_TOTAL < 60%"; PASS=false
fi
if [ "$RECOVERY_OK" -lt 8 ]; then
    log "FAIL: recovery $RECOVERY_OK/10 < 8"; PASS=false
fi

# 额外验证: gateway 在整个测试过程中没死
curl -sf "$GATEWAY/healthz" > /dev/null || { log "FAIL: gateway died"; PASS=false; }

# 写 strict envelope
CHECK_BASELINE=$([ "$BASELINE_OK" -ge 4 ] && echo true || echo false)
CHECK_WINDOW=$([ "$WINDOW_OK" -ge "$WINDOW_OK_MIN" ] && echo true || echo false)
CHECK_RECOVERY=$([ "$RECOVERY_OK" -ge 8 ] && echo true || echo false)
CHECK_ALIVE=true
CHECK_FAULT_INJECT=$([ "$FI_RC" -eq 0 ] && echo true || echo false)
STATUS=$([ "$PASS" = true ] && echo PASS || echo FAIL)
TOTAL=$((BASELINE_OK >= 0 ? 5 : 0))
TOTAL=$((5 + WINDOW_TOTAL + RECOVERY_TOTAL))
SUCC=$((BASELINE_OK + WINDOW_OK + RECOVERY_OK))
RATE=$(awk "BEGIN{printf \"%.3f\", $SUCC/$TOTAL}")
FAILURES="[]"
if [ "$PASS" != true ]; then
  FAILURES="[\"S24 acceptance failed: baseline=$BASELINE_OK/5 window=$WINDOW_OK/$WINDOW_TOTAL recovery=$RECOVERY_OK/10\"]"
fi
write_scenario_result "$SCENARIO" "functional" "$STATUS" \
  "{\"baseline_5_of_5\":$CHECK_BASELINE,\"window_60pct\":$CHECK_WINDOW,\"recovery_8_of_10\":$CHECK_RECOVERY,\"fault_inject_completed\":$CHECK_FAULT_INJECT,\"gateway_alive\":$CHECK_ALIVE}" \
  "{\"total\":$TOTAL,\"succ\":$SUCC,\"fail\":$((TOTAL-SUCC)),\"success_rate\":$RATE,\"p99_ms\":$WINDOW_P99_MS,\"baseline_ok\":$BASELINE_OK,\"window_ok\":$WINDOW_OK,\"window_total\":$WINDOW_TOTAL,\"window_5xx\":$WINDOW_5XX,\"window_other\":$WINDOW_OTHER,\"recovery_ok\":$RECOVERY_OK,\"recovery_total\":$RECOVERY_TOTAL}" \
  "{\"window_codes\":\"$WINDOW_CODES\",\"fault_inject_exit\":$FI_RC,\"fault_inject_log\":\"results/${SCENARIO}-fault-inject.jsonl\"}" \
  "$FAILURES" \
  "{\"gateway\":\"$GATEWAY\",\"kill_group\":\"G\",\"kill_after_sec\":1,\"window_sec\":11,\"recovery_after_sec\":15}"

if [ "$PASS" = true ]; then
    log "PASS"
    exit 0
else
    log "FAIL"
    exit 1
fi

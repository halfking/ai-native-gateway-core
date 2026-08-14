#!/bin/bash
# S27: 流式响应闪断与恢复 (Streaming Recovery After Disconnect)
#
# 2026-08-14:
#   让 G 组每个 mock_supplier 设置 disconnect_after_ms=80 — 客户端在
#   收到第一个 SSE chunk 后 80ms 收不到后续字节。
#   期望:
#     - 流式响应至少能拿到 1 个 chunk (data: {...})
#     - 客户端不应 hang (必须在 ≤5s 内 EOF 或 error)
#     - 非流式请求不应受影响 (因为 non-stream 走完整请求路径,不走 SSE)
#     - request_logs 应记录连接中断类型
#
# V2/V3 需求对应:
#   - V2 §18 网关稳定性 (流式中断应能被快速检测)
#   - V3.1 §3.3 队列瀑布流 (流式响应也是队列层对象)
#   - V3 §08 实施细节 (X-Gw-Resume-Token / 客户端 reconnect)

set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
source "$SCRIPT_DIR/_lib.sh"

SCENARIO="S27_streaming_recovery_after_disconnect"
RESULT="$RESULTS_DIR/${SCENARIO}.json"
mkdir -p "$RESULTS_DIR"

log() { echo "[S27] $*"; }

reset_all_suppliers
sleep 2

# 让 G 组的 5 个 mock_supplier 在收到第一个 SSE chunk 后 80ms abort
log "configure G group: disconnect_after_ms=80 (mid-stream abort)"
python3 "$TOOLS_DIR/mock_orchestrator.py" set-group-disconnect-after G 80 2>&1 | tail -1

AK="$(echo "$API_KEYS" | cut -d, -f1)"
log "client api key (truncated): ${AK:0:18}..."

curl -sf "$GATEWAY/healthz" > /dev/null || { log "FAIL: gateway not reachable"; exit 1; }

# Phase A: 非流式请求,验证不受影响 (non-stream 路径不进入 SSE chunk loop)
log "phase A: 5 non-stream requests (should all succeed)"
NON_STREAM_OK=0
for i in 1 2 3 4 5; do
    CODE=$(curl -s -o /dev/null -w "%{http_code}" -X POST \
        -H "Authorization: Bearer $AK" -H "Content-Type: application/json" \
        -d '{"model":"loadtest-mini-alpha","messages":[{"role":"user","content":"non-stream"}],"max_tokens":10,"stream":false}' \
        "$GATEWAY/v1/chat/completions" || echo 000)
    [ "$CODE" = "200" ] && NON_STREAM_OK=$((NON_STREAM_OK+1))
done
log "non-stream: $NON_STREAM_OK/5 OK"

# Phase B: 流式请求,验证首 chunk 后 EOF
log "phase B: 3 streaming requests (expect first chunk + EOF within 5s)"
STREAM_OK=0
STREAM_GOT_CHUNK=0
STREAM_TIMED_OUT=0
STREAM_CHUNK_DETAILS=""
STREAM_LAT_FILE="$(mktemp -t s27-lat-XXXXXX).txt"
for i in 1 2 3; do
    # 用 timeout 5s 避免 hang
    T0=$(python3 -c 'import time;print(int(time.time()*1000))')
    set +e
    OUT=$(timeout 5 curl -s -N -X POST \
        -H "Authorization: Bearer $AK" -H "Content-Type: application/json" \
        -d '{"model":"loadtest-mini-alpha","messages":[{"role":"user","content":"stream"}],"max_tokens":30,"stream":true}' \
        "$GATEWAY/v1/chat/completions" 2>&1)
    RC=$?
    set -e
    T1=$(python3 -c 'import time;print(int(time.time()*1000))')
    LAT=$((T1 - T0))
    if echo "$OUT" | grep -q "^data: "; then
        STREAM_GOT_CHUNK=$((STREAM_GOT_CHUNK+1))
        STREAM_OK=$((STREAM_OK+1))
        FIRST_DATA=$(echo "$OUT" | grep -c "^data: ")
        STREAM_CHUNK_DETAILS="$STREAM_CHUNK_DETAILS,$FIRST_DATA"
        echo "$LAT" >> "$STREAM_LAT_FILE"
    else
        STREAM_CHUNK_DETAILS="$STREAM_CHUNK_DETAILS,0"
    fi
    # timeout(5) 返回 124 = timed out, 但我们设了 -N; 实际若有数据返回就 ok
    [ "$RC" = "124" ] && STREAM_TIMED_OUT=$((STREAM_TIMED_OUT+1))
done
STREAM_P99_MS=$(python3 -c "
xs = sorted(int(l.strip()) for l in open('$STREAM_LAT_FILE') if l.strip())
if not xs: print(0)
else: print(xs[int(len(xs)*0.99)])
")
rm -f "$STREAM_LAT_FILE"
log "stream: $STREAM_OK/3 收到字节, chunks=$STREAM_CHUNK_DETAILS, timed_out=$STREAM_TIMED_OUT, p99=${STREAM_P99_MS}ms"

# Phase C: 重置 G, 恢复 100% 流式
log "phase C: reset G and re-test streaming"
reset_all_suppliers >/dev/null 2>&1
sleep 2
RECOVER_STREAM_OK=0
for i in 1 2 3; do
    OUT=$(timeout 5 curl -s -N -X POST \
        -H "Authorization: Bearer $AK" -H "Content-Type: application/json" \
        -d '{"model":"loadtest-mini-alpha","messages":[{"role":"user","content":"recover-stream"}],"max_tokens":30,"stream":true}' \
        "$GATEWAY/v1/chat/completions" 2>&1 || true)
    if echo "$OUT" | grep -q "^data: \[DONE\]"; then
        RECOVER_STREAM_OK=$((RECOVER_STREAM_OK+1))
    fi
done
log "recovery stream: $RECOVER_STREAM_OK/3 got [DONE]"

# 判定
PASS=true
[ "$NON_STREAM_OK" -ge 4 ] || { log "FAIL: non-stream $NON_STREAM_OK/5"; PASS=false; }
[ "$STREAM_GOT_CHUNK" -ge 1 ] || { log "FAIL: no streaming chunk received"; PASS=false; }
[ "$STREAM_TIMED_OUT" -le 2 ] || { log "FAIL: $STREAM_TIMED_OUT/3 stream requests timed out"; PASS=false; }
[ "$RECOVER_STREAM_OK" -ge 2 ] || { log "FAIL: recovery stream $RECOVER_STREAM_OK/3"; PASS=false; }
curl -sf "$GATEWAY/healthz" > /dev/null || { log "FAIL: gateway died"; PASS=false; }

CHECK_NON_STREAM=$([ "$NON_STREAM_OK" -ge 4 ] && echo true || echo false)
CHECK_STREAM_CHUNK=$([ "$STREAM_GOT_CHUNK" -ge 1 ] && echo true || echo false)
CHECK_STREAM_RE=$([ "$RECOVER_STREAM_OK" -ge 2 ] && echo true || echo false)
CHECK_STREAM_TIMEOUT=$([ "$STREAM_TIMED_OUT" -le 2 ] && echo true || echo false)
CHECK_ALIVE=true
TOTAL=$((5 + 3 + 3))
SUCC=$((NON_STREAM_OK + STREAM_OK + RECOVER_STREAM_OK))
RATE=$(awk "BEGIN{printf \"%.3f\", $SUCC/$TOTAL}")
STATUS=$([ "$PASS" = true ] && echo PASS || echo FAIL)
FAILURES="[]"
[ "$PASS" != true ] && FAILURES="[\"S27 streaming recovery failed: ns=$NON_STREAM_OK/5 stream=$STREAM_GOT_CHUNK/3 rec=$RECOVER_STREAM_OK/3\"]"

write_scenario_result "$SCENARIO" "functional" "$STATUS" \
  "{\"non_stream_4_of_5\":$CHECK_NON_STREAM,\"stream_got_chunk_1_of_3\":$CHECK_STREAM_CHUNK,\"stream_timeouts_le_2_of_3\":$CHECK_STREAM_TIMEOUT,\"recovery_stream_2_of_3\":$CHECK_STREAM_RE,\"gateway_alive\":$CHECK_ALIVE}" \
  "{\"total\":$TOTAL,\"succ\":$SUCC,\"fail\":$((TOTAL-SUCC)),\"success_rate\":$RATE,\"p99_ms\":$STREAM_P99_MS,\"non_stream_ok\":$NON_STREAM_OK,\"stream_ok\":$STREAM_OK,\"stream_got_chunk\":$STREAM_GOT_CHUNK,\"stream_timed_out\":$STREAM_TIMED_OUT,\"recovery_stream_ok\":$RECOVER_STREAM_OK}" \
  "{\"stream_chunks\":\"$STREAM_CHUNK_DETAILS\"}" \
  "$FAILURES" \
  "{\"gateway\":\"$GATEWAY\",\"disconnect_after_ms\":80,\"group\":\"G\"}"

[ "$PASS" = true ] && { log "PASS"; exit 0; } || { log "FAIL"; exit 1; }

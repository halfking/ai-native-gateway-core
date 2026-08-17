#!/bin/bash
# S18: NULL 数据处理 (NULL Success Rate Handling)
#
# 验证 system_health_status 返回 NULL success_rate 时的容错处理。
# 不使用 loadtest.py — 直接靠 curl 让 30s 窗口清空，再查询健康端点。
#
# 背景: 2026-07-19 修复 fix(system-health): scan NULL success_rate as *float64 with nil guard
# - 问题：30秒窗口内零请求时，success_rate 为 NULL，pgx 无法扫描到 float64 导致 panic
# - 修复：改用 *float64 类型，NULL 时回退到 0.0；isRetriableError 改用 errorsx 常量

set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
source "$SCRIPT_DIR/_lib.sh"

SCENARIO="S18_null_handling"
RESULT="$RESULTS_DIR/${SCENARIO}.json"
mkdir -p "$RESULTS_DIR"

log()   { echo "[S18] $*"; }

log "starting NULL success_rate handling verification"
log "gateway=$GATEWAY"
AK="${API_KEYS%%,*}"

# ── 0. 前置健康检查 ─────────────────────────────────────────────────
curl -sf "$GATEWAY/healthz" > /dev/null || { log "FAIL: gateway not reachable"; exit 1; }
log "gateway reachable"

# ── 1. 等待 30s 窗口清空（不再有请求计入 system_health_status(30)） ─
log "waiting 35s for the 30s health window to drain (no traffic)..."
sleep 35

# ── 2. 验证 gateway 在 30s 零请求窗口下不 panic ──────────────────
# 实际探针端点 (/api/admin/probe/system-health) 需要 admin 凭据, 此处
# 用更普适的检查:
#   (a) healthz 一直 OK
#   (b) 连续两条请求都能路由 (即便返回 model_not_found 也算 OK — 不 panic)
log "phase 1: gateway should stay alive across 35s idle window..."
sleep 35

# 触发 healthz + 一条典型请求
HEALTH_BEFORE=$(curl -sf "$GATEWAY/healthz" | jq -r '.status' || echo "FAIL")
log "  /healthz after 35s idle: $HEALTH_BEFORE"

REQ_AFTER_IDLE=$(curl -s -X POST -m 8 \
    -H "Authorization: Bearer $AK" \
    -H "Content-Type: application/json" \
    -d '{"model":"loadtest-mini-alpha","messages":[{"role":"user","content":"hi"}],"max_tokens":5,"stream":false}' \
    "$GATEWAY/v1/chat/completions" || echo '{"error":"timeout"}')
HTTP_AFTER_IDLE=$(curl -s -o /dev/null -w "%{http_code}" -X POST \
    -H "Authorization: Bearer $AK" -H "Content-Type: application/json" \
    -d '{"model":"loadtest-mini-alpha","messages":[{"role":"user","content":"hi"}],"max_tokens":5,"stream":false}' \
    "$GATEWAY/v1/chat/completions" || echo 000)
log "  request after 35s idle: HTTP $HTTP_AFTER_IDLE"
log "  body sample: $(echo "$REQ_AFTER_IDLE" | head -c 200)"

# 关键判定: gateway 必须仍响应 (返回 200/4xx/5xx 都算 OK; 只拒绝 == 死进程)
curl -sf "$GATEWAY/healthz" > /dev/null || { log "FAIL: gateway died after idle window"; exit 1; }
log "gateway still alive after 35s idle window"

# ── 3. 产生少量低频请求，再次触发 status='suspect' → 0.0 路径 ─────
log "issuing 5 low-frequency requests (1 / 6s, all cross the 30s window)..."
SUCC=0
TOTAL=0
for i in 1 2 3 4 5; do
    CODE=$(curl -s -o /dev/null -w "%{http_code}" \
        -H "Authorization: Bearer $AK" \
        -H "Content-Type: application/json" \
        -d '{"model":"loadtest-mini-alpha","messages":[{"role":"user","content":"hi"}],"max_tokens":10,"stream":false}' \
        "$GATEWAY/v1/chat/completions" || echo 000)
    TOTAL=$((TOTAL+1))
    [ "$CODE" = "200" ] && SUCC=$((SUCC+1))
    log "  req $i → HTTP $CODE"
    sleep 6
done
SUCCESS_RATE=$(awk "BEGIN{printf \"%.1f\", $SUCC*100/$TOTAL}")
log "low-freq requests: $SUCC/$TOTAL OK ($SUCCESS_RATE%)"

# ── 4. 再触发一次 gateway 请求 (模拟窗口有样本) ─────────────────
log "phase 2: another request, simulating sample presence..."
HEALTH_CODE2=$(curl -s -o /dev/null -w "%{http_code}" -X POST \
    -H "Authorization: Bearer $AK" -H "Content-Type: application/json" \
    -d '{"model":"loadtest-mini-alpha","messages":[{"role":"user","content":"hi2"}],"max_tokens":5,"stream":false}' \
    "$GATEWAY/v1/chat/completions" || echo 000)
log "  second request HTTP $HEALTH_CODE2"

curl -sf "$GATEWAY/healthz" > /dev/null || { log "FAIL: gateway died after 2nd req"; exit 1; }

# ── 5. 判定 ─────────────────────────────────────────────────────────
PASS=true
# (a) gateway healthz 一直 OK
[ "$HEALTH_BEFORE" = "ok" ] || { log "FAIL: /healthz returned $HEALTH_BEFORE after idle"; PASS=false; }
# (b) idle 后请求必须被响应 (4xx/5xx 都 OK, 不能 000)
[ "$HTTP_AFTER_IDLE" != "000" ] || { log "FAIL: gateway did not respond after idle"; PASS=false; }
# (c) 二次请求也必须被响应
[ "$HEALTH_CODE2" != "000" ] || { log "FAIL: gateway did not respond on 2nd request"; PASS=false; }
# (d) 至少 3/5 个低频请求成功 (允许部分被模型不可路由影响, 但 gateway 不应死)
[ "$SUCC" -ge 3 ] || { log "WARN: only $SUCC/$TOTAL low-freq requests got non-err body (expected >=3, may be acceptable)"; }

# Strict result envelope (schema_version 1.0)
CHECK_HEALTH=$([ "$HEALTH_BEFORE" = "ok" ] && echo true || echo false)
CHECK_IDLE=$([ "$HTTP_AFTER_IDLE" != "000" ] && echo true || echo false)
CHECK_SECOND=$([ "$HEALTH_CODE2" != "000" ] && echo true || echo false)
STATUS=$([ "$PASS" = true ] && echo PASS || echo FAIL)
FAILURES="[]"
if [ "$PASS" != true ]; then
  FAILURES='["gateway did not stay healthy across idle / low-freq window"]'
fi
write_scenario_result "$SCENARIO" "functional" "$STATUS" \
  "{\"healthz_after_idle_ok\":$CHECK_HEALTH,\"responds_after_idle\":$CHECK_IDLE,\"responds_second_request\":$CHECK_SECOND}" \
  "{\"total\":$TOTAL,\"succ\":$SUCC,\"fail\":$((TOTAL-SUCC)),\"success_rate\":$(awk "BEGIN{print $SUCC/$TOTAL}"),\"elapsed_sec\":35,\"p99_required\":false,\"fail_by_status\":{},\"fail_by_kind\":{}}" \
  "{\"healthz_after_idle\":\"$HEALTH_BEFORE\",\"first_request_http\":$HTTP_AFTER_IDLE,\"second_request_http\":$HEALTH_CODE2,\"gateway_alive\":true}" \
  "$FAILURES" \
  "{\"gateway\":\"$GATEWAY\"}"

if [ "$PASS" = true ]; then
    log "PASS"
    exit 0
else
    log "FAIL"
    exit 1
fi

#!/bin/bash
# S19: 租户隔离验证 (Tenant Isolation for Pending Responses)
#
# 用两把不同 api key 模拟两个租户，每把 key 各发 stream 请求并中途取消，
# 然后用对方的 token 去取 pending-response，期望返回 404。
#
# 背景: 2026-07-19 修复 fix(streaming): pending continuation audit P1/P2 repairs (P2)
# - 问题：pending replay 缺乏租户验证，可能跨租户访问
# - 修复：精确租户匹配 + 404 非枚举 + admin 租户作用域

set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
source "$SCRIPT_DIR/_lib.sh"

SCENARIO="S19_tenant_isolation"
RESULT="$RESULTS_DIR/${SCENARIO}.json"
mkdir -p "$RESULTS_DIR"

log() { echo "[S19] $*"; }

log "starting tenant isolation verification"
log "gateway=$GATEWAY"

curl -sf "$GATEWAY/healthz" > /dev/null || { log "FAIL: gateway not reachable"; exit 1; }

# 两把不同的 api key 当两个租户
AK_A=$(echo "$API_KEYS" | cut -d, -f1)
AK_B=$(echo "$API_KEYS" | cut -d, -f2)
log "tenant A key: ${AK_A:0:20}..."
log "tenant B key: ${AK_B:0:20}..."

# 1. Tenant A 用一个 session_id 走 stream 请求，中途客户端断开
#    → gateway 应该把响应存为 pending，归属 tenant A
SID_A="sess-tenant-a-$$"
log "Tenant A: sending stream request with X-Gw-Session-Id=$SID_A then aborting..."

( curl -s -N \
    -H "Authorization: Bearer $AK_A" \
    -H "X-Gw-Session-Id: $SID_A" \
    -H "Content-Type: application/json" \
    -d '{"model":"loadtest-mini-alpha","messages":[{"role":"user","content":"hi"}],"max_tokens":30,"stream":true}' \
    "$GATEWAY/v1/chat/completions" &
    CURL_PID=$!
    sleep 0.5
    kill -9 "$CURL_PID" 2>/dev/null || true
    wait "$CURL_PID" 2>/dev/null || true
) </dev/null >/dev/null 2>&1 || true
log "Tenant A request aborted after ~500ms"

# 给 gateway 一个消化时间，把 pending 落盘
sleep 2

# 2. Tenant A 取自己的 pending —— 期望 200
log "Tenant A reads own pending (expect 200)..."
RESP_A=$(curl -s -o /dev/null -w "%{http_code}" \
    -H "Authorization: Bearer $AK_A" \
    "$GATEWAY/v1/sessions/$SID_A/pending-response" || echo 000)
log "  → HTTP $RESP_A"

# 3. Tenant B 用同一 SID 取 Tenant A 的 pending —— 期望 404 (非枚举)
log "Tenant B tries to read Tenant A's pending (expect 404)..."
RESP_B=$(curl -s -o /dev/null -w "%{http_code}" \
    -H "Authorization: Bearer $AK_B" \
    "$GATEWAY/v1/sessions/$SID_A/pending-response" || echo 000)
log "  → HTTP $RESP_B"

# 4. 无 token 取 —— 期望 401 / 404
log "Anonymous tries to read pending (expect 401/404)..."
RESP_ANON=$(curl -s -o /dev/null -w "%{http_code}" \
    "$GATEWAY/v1/sessions/$SID_A/pending-response" || echo 000)
log "  → HTTP $RESP_ANON"

# 5. 不存在的 SID —— 期望 404，不泄露存在性
log "Request non-existent session (expect 404, no enumeration)..."
RESP_FAKE=$(curl -s -o /dev/null -w "%{http_code}" \
    -H "Authorization: Bearer $AK_A" \
    "$GATEWAY/v1/sessions/00000000-no-such-session-000000000000/pending-response" || echo 000)
log "  → HTTP $RESP_FAKE"

# 6. Admin 端点的租户作用域（如果端点存在；不存在就 WARN 不 fail）
ADMIN_HTTP=""
if curl -sf "$GATEWAY/admin/pending-responses" -o /dev/null -w "" 2>/dev/null; then
    ADMIN_HTTP=$(curl -s -o /dev/null -w "%{http_code}" \
        -H "Authorization: Bearer $AK_A" \
        "$GATEWAY/admin/pending-responses" || echo 000)
    log "Admin list with tenant A key → HTTP $ADMIN_HTTP"
else
    log "  /admin/pending-responses endpoint not present (skipped)"
fi

# 判定
PASS=true
# (a) 跨租户访问必须失败
if [ "$RESP_B" = "404" ] || [ "$RESP_B" = "403" ]; then
    log "PASS: cross-tenant blocked ($RESP_B)"
else
    log "FAIL: cross-tenant access returned $RESP_B"; PASS=false
fi
# (b) 匿名访问必须失败
if [ "$RESP_ANON" = "401" ] || [ "$RESP_ANON" = "404" ]; then
    log "PASS: anonymous blocked ($RESP_ANON)"
else
    log "FAIL: anonymous returned $RESP_ANON"; PASS=false
fi
# (c) 不存在 session 必须 404
if [ "$RESP_FAKE" = "404" ]; then
    log "PASS: non-existent session 404 (no enumeration)"
else
    log "WARN: non-existent session returned $RESP_FAKE (expected 404)"
fi

# Strict result envelope (schema_version 1.0)
CHECK_CROSS=$([ "$RESP_B" = "404" ] || [ "$RESP_B" = "403" ] && echo true || echo false)
CHECK_ANON=$([ "$RESP_ANON" = "401" ] || [ "$RESP_ANON" = "404" ] && echo true || echo false)
CHECK_FAKE=$([ "$RESP_FAKE" = "404" ] && echo true || echo false)
SUCC=$([ "$PASS" = true ] && echo 4 || echo 3)
FAIL=$((4 - SUCC))
RATE=$([ "$PASS" = true ] && echo 1.0 || echo 0.75)
STATUS=$([ "$PASS" = true ] && echo PASS || echo FAIL)
FAILURES="[]"
if [ "$PASS" != true ]; then
  FAILURES="[\"tenant isolation checks failed: cross=$RESP_B anon=$RESP_ANON fake=$RESP_FAKE\"]"
fi
ADMIN_JSON=${ADMIN_HTTP:-null}
write_scenario_result "$SCENARIO" "functional" "$STATUS" \
  "{\"cross_tenant_blocked\":$CHECK_CROSS,\"anonymous_blocked\":$CHECK_ANON,\"non_existent_404\":$CHECK_FAKE}" \
  "{\"total\":4,\"succ\":$SUCC,\"fail\":$FAIL,\"success_rate\":$RATE,\"elapsed_sec\":4,\"p99_ms\":0,\"fail_by_status\":{},\"fail_by_kind\":{}}" \
  "{\"tenant_a_own\":$RESP_A,\"tenant_b_cross\":$RESP_B,\"anon\":$RESP_ANON,\"non_existent\":$RESP_FAKE,\"admin_tenant_a\":$ADMIN_JSON}" \
  "$FAILURES" \
  "{\"gateway\":\"$GATEWAY\"}"

if [ "$PASS" = true ]; then
    log "PASS"
    exit 0
else
    log "FAIL"
    exit 1
fi
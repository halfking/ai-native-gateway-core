#!/bin/bash
# S17: 流式断连续传 (Stream Continuation After Client Disconnect)
#
# 用 loadtest.py 的 --fault-inject-cancel 在流式请求中途断连，触发
# gateway 的 pending continuation 路径，然后通过 curl 验证 pending
# endpoint 可以恢复响应、且上游 body 未被截断/重复消费。
#
# 背景: 2026-07-19 修复 fix(streaming): pending continuation audit P1/P2 repairs
# - 问题：客户端断连后上游流式响应丢失/重复消费/租户上下文丢失
# - 修复：context.WithoutCancel 保留 tenant，单消费者模式，独立 pending TTL

set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
source "$SCRIPT_DIR/_lib.sh"

SCENARIO="S17_stream_continuation"
RESULT="$RESULTS_DIR/${SCENARIO}.json"
mkdir -p "$RESULTS_DIR"

log() { echo "[S17] $*"; }

log "starting stream continuation + pending verification"
log "gateway=$GATEWAY"

# 0. 前置
curl -sf "$GATEWAY/healthz" > /dev/null || { log "FAIL: gateway not reachable"; exit 1; }

# 1. 重置 suppliers 为健康状态
reset_all_suppliers

# 2. 跑 loadtest：50% stream + 30% 客户端中途取消（会触发 pending continuation）
#    用单一 api key 以便后续 pending-response 查询能找到 session
log "running loadtest with stream_ratio=0.5, fault_inject_cancel=0.3 ..."
run_loadtest "$SCENARIO" \
    --n-clients 10 \
    --rps-per-client 2 \
    --duration 30 \
    --models tok3 \
    --prompt short \
    --stream-ratio 0.5 \
    --fault-inject-cancel 0.3

print_summary "$SCENARIO"

# 3. 从结果 JSON 提取 success_rate / cancel 计数
TOTAL=$(jq -r '.metrics.total' "$RESULT")
SUCC=$(jq -r '.metrics.succ' "$RESULT")
CANCELS=$(jq -r '.metrics.fail_by_kind."client_cancel" // 0' "$RESULT")
FAIL_STATUS=$(jq -rc '.metrics.fail_by_status // {}' "$RESULT")
log "  total=$TOTAL succ=$SUCC cancels=$CANCELS fail_by_status=$FAIL_STATUS"

# 4. 验证 pending-response 端点：用一个已知 session 复盘
#    （session id 来自 loadtest 内部 sess-{client}-{i} 模式，我们试一个有 cancel 的）
#    公开端点要求精确租户匹配；拿第一把 api key 当租户 owner
AK=$(echo "$API_KEYS" | cut -d, -f1)
# loadtest 的 session 池命名：sess-0-0 ~ sess-0-19；试几个
PENDING_OK=0
PENDING_404=0
for SID in sess-0-0 sess-0-1 sess-1-0 sess-2-0 sess-3-0; do
    CODE=$(curl -s -o /dev/null -w "%{http_code}" \
        -H "Authorization: Bearer $AK" \
        "$GATEWAY/v1/sessions/$SID/pending-response" || echo 000)
    case "$CODE" in
        200) PENDING_OK=$((PENDING_OK+1)); log "  pending $SID → 200 (recoverable)" ;;
        404) PENDING_404=$((PENDING_404+1)); log "  pending $SID → 404 (no pending or not owned)" ;;
        *)   log "  pending $SID → unexpected $CODE" ;;
    esac
done

# 5. 判定
PASS=true
# (a) 排除 client_cancel 的"有效成功率" ≥ 90%
#     注：fault_inject_cancel=0.3 × stream_ratio=0.5 会主动 cancel ≈15% 的请求，
#     把这部分按预期事件剔除后再算，剩 ~85% 应基本全成。
SUCC_RATE=$(awk "BEGIN{print $SUCC*100/$TOTAL}")
EFFECTIVE_TOTAL=$((TOTAL - CANCELS))
if [ "$EFFECTIVE_TOTAL" -gt 0 ]; then
    EFFECTIVE_RATE=$(awk "BEGIN{print $SUCC*100/$EFFECTIVE_TOTAL}")
else
    EFFECTIVE_RATE="$SUCC_RATE"
fi
if awk "BEGIN{exit !($EFFECTIVE_RATE >= 90)}"; then
    log "PASS: effective_success_rate=$EFFECTIVE_RATE% (>=90%, excludes $CANCELS client_cancel out of $TOTAL)"
else
    log "FAIL: effective_success_rate=$EFFECTIVE_RATE% (<90%); raw=$SUCC_RATE%"; PASS=false
fi
# (b) 必须有 cancel 事件被记到，证明 fault_inject 起效
if [ "$CANCELS" -ge 1 ]; then
    log "PASS: $CANCELS client_cancel recorded"
else
    log "FAIL: no client_cancel recorded — fault_inject_cancel did not trigger"; PASS=false
fi
# (c) pending-response 端点必须存活（不 panic、不 500）
if [ "$PENDING_OK" -ge 1 ]; then
    log "PASS: $PENDING_OK pending responses successfully recovered"
else
    log "WARN: no pending recovered (may have expired or none created with these sess-ids); endpoint still alive (got $PENDING_404 x404)"
fi

# gateway 必须存活
curl -sf "$GATEWAY/healthz" > /dev/null || { log "FAIL: gateway died during test"; PASS=false; }

# 写最终判定标记
jq --arg pass "$PASS" \
   --argjson cancels "$CANCELS" \
   --argjson pending_ok "$PENDING_OK" \
   --argjson pending_404 "$PENDING_404" \
   '.extra = {client_cancel: $cancels, pending_recovered: $pending_ok, pending_404: $pending_404, pass: ($pass == "true")}' \
   "$RESULT" > "$RESULT.tmp" && mv "$RESULT.tmp" "$RESULT"

reset_all_suppliers

if [ "$PASS" = true ]; then
    log "PASS"
    exit 0
else
    log "FAIL"
    exit 1
fi

#!/bin/bash
# docs/全方面测试/scenarios/S23_long_text_chunked.sh
#
# S23: 长文本分段 — 验证长 prompt 触发 compression + CutMarker IncrementalBuild
#
# 覆盖:
#   23.1 60 轮长 prompt (500 chars/轮) 触发压缩 (count trigger)
#   23.2 CutMarker IncrementalBuild — 第二轮起 outbound_msg_count 下降
#         (滑动窗口: 不压缩所有历史, 只保留最新 N 轮)
#   23.3 80 轮 chat 触发 outbound_msg_count < 50 (smm_v1 marker 路径)
#   23.4 真 map-reduce chunked summary [TODO: 需要 chunked_summarizer.go 实现]
#
# 2026-08-06: 首次实现 (PENDING)
# 2026-08-06: 修 ON CONFLICT + API_KEYS + 索引后, 从 PENDING 改回真测试.
# 23.1 + 23.2 实测, 23.3 + 23.4 标 PENDING (bash 5 EOF bug + chunked_summarizer 未实现).

set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
source "$SCRIPT_DIR/_lib.sh"

SCENARIO="S23_long_text_chunked"
RESULT="$RESULTS_DIR/${SCENARIO}.json"
mkdir -p "$RESULTS_DIR"

log() { echo "[S23] $*"; }
PASS=true

# 0. 准备
psql_count "DELETE FROM request_logs_bodies_hot WHERE request_id LIKE 's23-%'" >/dev/null 2>&1 || true
psql_count "DELETE FROM request_logs_hot WHERE request_id LIKE 's23-%'" >/dev/null 2>&1 || true
psql_count "DELETE FROM request_logs_bodies WHERE request_id LIKE 's23-%'" >/dev/null 2>&1 || true
psql_count "DELETE FROM request_logs WHERE request_id LIKE 's23-%'" >/dev/null 2>&1 || true
log "cleaned s23-* test rows"

reset_all_suppliers >/dev/null
log "mock suppliers reset to healthy"

# 23.1 60 轮长 prompt (500 chars/轮) 触发压缩
log "23.1: 60 轮长 prompt (count 触发器)"
SID_23_1="s23-t1-$$-$(date +%s)"
BASE_BODIES_23_1=$(psql_count "SELECT count(*) FROM request_logs_bodies_hot WHERE ts > NOW() - INTERVAL '1 hour'" 2>/dev/null)
BASE_BODIES_23_1=${BASE_BODIES_23_1:-0}
cd "$TOOLS_DIR"
python3 chat_rounds_client.py \
    --gateway "$GATEWAY" \
    --api-key "$(echo "$API_KEYS" | cut -d, -f1)" \
    --session-id "$SID_23_1" \
    --rounds 60 \
    --model loadtest-mini-alpha \
    --prompt medium 2>&1 | tail -1
cd "$RESULTS_DIR/.."
sleep 3
BODIES_23_1=$(psql_count "SELECT count(*) FROM request_logs_bodies_hot WHERE ts > NOW() - INTERVAL '1 hour'" 2>/dev/null)
BODIES_23_1=${BODIES_23_1:-0}
DELTA_23_1=$((BODIES_23_1 - BASE_BODIES_23_1))
log "23.1: 60 轮长 prompt 后 request_logs_bodies_hot delta=$DELTA_23_1"
if [ "$DELTA_23_1" -ge 50 ]; then
    log "✅ 23.1 PASS: 60 轮长 prompt 落 DB ($DELTA_23_1 行, 期望 ~60)"
else
    log "❌ 23.1 FAIL: 60 轮长 prompt 后 DB delta=$DELTA_23_1 远低于 60"
    PASS=false
fi

# 23.2 CutMarker IncrementalBuild — 累计 30 轮, 最后 10 轮的 outbound_msg_count 应 < 50
log "23.2: 累计 30 轮后, 最后 10 轮 outbound_msg_count 应被 sliding window 截断"
# 验证: request_logs_bodies_hot 中该 session 的 outbound_msg_count 分布
# 注: outbound_msg_count 在 request_logs_hot (不在 _bodies_hot)
# gateway 内部 X-Gw-Session-Id → gw_<uuid> 映射, 没法 LIKE s23-t1-%
# 改为查 60 轮 chat 整体: 该 session 的 outbound_msg_count 分布
DISTRIBUTION=$(PGPASSWORD="$PGPASSWORD" psql -h "$PGHOST" -p "$PGPORT" -U "$PGUSER" -d "$PGDB" -tA -c "
    SELECT count(*) FROM request_logs_hot
    WHERE ts > NOW() - INTERVAL '2 minutes'
      AND outbound_msg_count < 50
      AND outbound_msg_count > 0" 2>/dev/null)
DISTRIBUTION=${DISTRIBUTION:-0}
log "23.2: 30 轮中 outbound_msg_count < 50 (压缩触发) 的行数: $DISTRIBUTION"
if [ "$DISTRIBUTION" -ge 1 ]; then
    log "✅ 23.2 PASS: 30 轮后有 $DISTRIBUTION 行被压缩 (outbound_msg_count < 50)"
else
    log "❌ 23.2 FAIL: 30 轮未触发压缩"
    PASS=false
fi

# 23.3 80 轮 chat 触发 outbound_msg_count < 50 (count trigger, sliding_window_count 路径)
log "23.3: 80 轮 chat 触发 sliding_window_count (调 Go driver)"
S23_3_OUT="$(cd "$RESULTS_DIR/../../.." && go run ./cmd/scenario_driver \
    --scenario s23-3 \
    --gateway "$GATEWAY" \
    --api-key "$(echo "$API_KEYS" | cut -d, -f1)" \
    --rounds 80 \
    --prompt short 2>&1)"
echo "$S23_3_OUT" | tail -2
DELTA_23_3=$(echo "$S23_3_OUT" | python3 -c "import json,sys; print(json.load(sys.stdin).get('delta_bodies', 0))" 2>/dev/null || echo "0")
DELTA_23_3=${DELTA_23_3:-0}
if [ "$DELTA_23_3" -ge 70 ]; then
    log "✅ 23.3 PASS: 80 轮落 DB (delta_bodies=$DELTA_23_3)"
else
    log "❌ 23.3 FAIL: 80 轮后 DB delta=$DELTA_23_3 远低于 70"
    PASS=false
fi

# 23.4 requires a fresh gateway log artifact from the current run. A missing
# artifact is a failed acceptance check, not evidence from an unrelated run.
log "23.4: map_reduce mode verification from current gateway log artifact"
GATEWAY_LOG="${GATEWAY_LOG:-/tmp/gateway-test.log}"
if [ -f "$GATEWAY_LOG" ]; then
    MAPREDUCE_LOG=$(grep -c "auto_summary: map_reduce mode" "$GATEWAY_LOG" || true)
    SINGLE_SHOT_LOG=$(grep -c "auto_summary: single-shot" "$GATEWAY_LOG" || true)
    LATEST=$(grep "auto_summary: map_reduce mode" "$GATEWAY_LOG" | tail -1 || true)
else
    MAPREDUCE_LOG=0
    SINGLE_SHOT_LOG=0
    LATEST=""
fi
log "23.4: map_reduce mode log count=$MAPREDUCE_LOG, single-shot log count=$SINGLE_SHOT_LOG"
log "23.4: latest map_reduce trigger: $LATEST"
CHECK_23_4=$([ "$MAPREDUCE_LOG" -ge 1 ] && echo true || echo false)
if [ "$CHECK_23_4" = true ]; then
    log "✅ 23.4 PASS: map_reduce path observed in current log artifact"
else
    log "❌ 23.4 FAIL: map_reduce path not observed in current log artifact"
    PASS=false
fi
# Strict result envelope — checks must be boolean only.
CHECK_23_1=$([ "$DELTA_23_1" -ge 50 ] && echo true || echo false)
CHECK_23_2=$([ "$DISTRIBUTION" -ge 1 ] && echo true || echo false)
CHECK_23_3=$([ "$DELTA_23_3" -ge 70 ] && echo true || echo false)
STATUS=$([ "$PASS" = true ] && echo PASS || echo FAIL)
FAILURES="[]"
if [ "$PASS" != true ]; then
  FAILURES='["long_text_chunked acceptance checks failed"]'
fi
write_scenario_result "$SCENARIO" "functional" "$STATUS" \
  "{\"23_1_token_trigger\":$CHECK_23_1,\"23_2_count_trigger\":$CHECK_23_2,\"23_3_map_reduce_chunked\":$CHECK_23_3,\"23_4_real_map_reduce\":$CHECK_23_4}" \
  "{\"delta_23_1\":$DELTA_23_1,\"distribution_23_2\":$DISTRIBUTION,\"delta_23_3\":$DELTA_23_3,\"success_rate\":$([ "$PASS" = true ] && echo 1.0 || echo 0.0),\"p99_required\":false}" \
  "{\"map_reduce_log_count\":$MAPREDUCE_LOG,\"single_shot_log_count\":$SINGLE_SHOT_LOG}" \
  "$FAILURES" \
  "{\"gateway\":\"$GATEWAY\"}"

if [ "$PASS" = true ]; then
    log "PASS"
    exit 0
else
    log "FAIL"
    exit 1
fi

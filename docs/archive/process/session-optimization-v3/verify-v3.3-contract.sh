#!/usr/bin/env bash
# verify-v3.3-contract.sh — V3.3-OBS 契约漂移检查（15号风格）
#
# 三方断言：后端代码 ↔ 13号契约文档 ↔ 前端 store/组件。
# 任一侧漂移（事件名、字段键、动作词汇表、上限、预算、词表映射）即 FAIL。
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
DOCS="$ROOT/docs/会话优化v3"
ADMIN="$ROOT/admin"
WEB="$ROOT/web/src"
PASS=0
FAIL=0

ok() { printf 'PASS %s\n' "$1"; PASS=$((PASS + 1)); }
bad() { printf 'FAIL %s\n' "$1"; FAIL=$((FAIL + 1)); }

# check <label> <file> <pattern>
check() {
  local label="$1" file="$2" pattern="$3"
  if rg -q --fixed-strings "$pattern" "$file"; then
    ok "$label"
  else
    bad "$label (missing '$pattern' in ${file#$ROOT/})"
  fi
}

# check_re <label> <file> <regex>
check_re() {
  local label="$1" file="$2" pattern="$3"
  if rg -q "$pattern" "$file"; then
    ok "$label"
  else
    bad "$label (regex '$pattern' not found in ${file#$ROOT/})"
  fi
}

DOC13="$DOCS/13-V3.2实际API与SSE契约.md"
SSE="$ADMIN/live_stream_sse.go"
LC="$ADMIN/live_stream_lifecycle.go"
RS="$ADMIN/live_stream_redis_store.go"
LA="$ROOT/internal/liveactions/liveactions.go"
STORE="$WEB/composables/liveStreamStore.ts"

# ── 1. SSE 事件类型（后端发射 ↔ 文档 ↔ 前端消费） ─────────────────────────────
for ev in request_lifecycle child_request; do
  check "backend emits $ev" "$LC" "\"$ev\""
  check "doc13 freezes $ev" "$DOC13" "\`$ev\`"
done
check "frontend consumes request_lifecycle" "$STORE" "'request_lifecycle'"
check "frontend consumes child_request" "$STORE" "'child_request'"

# ── 2. envelope 载荷键 ───────────────────────────────────────────────────────
check "envelope action key" "$SSE" 'Action any `json:"action,omitempty"`'
check "envelope parent_request_id key" "$SSE" 'ParentRequestID string `json:"parent_request_id,omitempty"`'

# ── 3. 动作词汇表（13 种，internal/liveactions 冻结表） ───────────────────────
for a in arrive route_resolved model_enqueued credential_selected node_enqueued \
         node_selected upstream_request first_byte reply node_switch model_switch \
         state_change no_route; do
  check "action vocab: $a" "$LA" "\"$a\""
done

# ── 4. 回放上限 / 推送预算 / Redis 键 ────────────────────────────────────────
check "replay cap 200" "$LC" 'defaultActionReplayLimit = 200'
check "poll tick 250ms (2× ≤ 500ms budget)" "$LC" 'defaultActionPollInterval = 250 * time.Millisecond'
check "actions redis key" "$LA" 'RedisKey = "llmgw:live:actions"'
check "doc13 records 500ms budget" "$DOC13" '≤500ms'

# ── 5. state_change 通道排除（24号 §2） ───────────────────────────────────────
check "lifecycle excludes node-dimension events" "$LC" 'func isRequestScopedAction'
check_re "doc13 records exclusion" "$DOC13" 'state_change.*不走本通道|不走.*通道.*state_change'

# ── 6. requestType 冻结词表映射 ──────────────────────────────────────────────
check "normalizeLiveRequestType exists" "$LC" 'func normalizeLiveRequestType'
for v in title summary sensitive_word probe unknown; do
  check "requestType enum $v" "$LC" "return \"$v\""
done
check "doc13 freezes enum" "$DOC13" 'chat | title | summary | sensitive_word | probe | unknown'

# ── 7. camelCase alias（零值不冒充） ─────────────────────────────────────────
check "camel alias parentRequestId" "$LC" '"parentRequestId"'
check "camel alias requestType" "$LC" '"requestType"'

# ── 8. Redis payload 保留 parent/type（remote hub 重建） ─────────────────────
check "payload keeps parent_request_id" "$RS" 'ParentRequestID string `json:"parent_request_id,omitempty"`'

# ── 9. BE3 pipeline 层（后端 ↔ 前端 ↔ 文档 CURRENT） ─────────────────────────
check "backend pipeline struct" "$SSE" 'WaitingMsP50 *int64 `json:"waitingMsP50,omitempty"`'
check "frontend pipeline type" "$STORE" 'waitingMsP50?: number | null'
check "frontend sourceVersion type" "$STORE" 'sourceVersion?: number'
check "doc13 pipeline CURRENT" "$DOC13" 'pipeline 层由 TARGET 升级为 CURRENT'

# ── 10. BE4 节点投影字段（后端 ↔ 前端） ──────────────────────────────────────
for f in fp_disabled disable_kind system_recover_at last_error_at; do
  check "backend node field $f" "$SSE" "$f"
  check "frontend node field $f" "$STORE" "$f?:"
done

# ── 11. 前端消费门禁（渲染端点/动作索引） ────────────────────────────────────
check "store exposes actions index" "$STORE" 'getRequestActions'
check "store exposes children index" "$STORE" 'getRequestChildren'
[[ -f "$WEB/components/ActionTimeline.vue" ]] && ok "ActionTimeline component exists" || bad "ActionTimeline component exists"
[[ -f "$WEB/components/NodeOpsRow.vue" ]] && ok "NodeOpsRow component exists" || bad "NodeOpsRow component exists"
check "NodeOpsRow uses test-now endpoint" "$WEB/components/NodeOpsRow.vue" '/api/admin/providers/'
check "NodeOpsRow uses emergency-repair" "$WEB/components/NodeOpsRow.vue" 'emergencyRepair'

# ── 12. detail 摊平（不保留 detail 包装键） ─────────────────────────────────
check "backend flattens detail" "$LC" 'flattenActionEvent'
check "doc13 records flattening" "$DOC13" '摊平到事件顶层'

printf '\nV3.3 contract drift: PASS=%d FAIL=%d\n' "$PASS" "$FAIL"
if [[ "$FAIL" -ne 0 ]]; then
  exit 1
fi

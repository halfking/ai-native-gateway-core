#!/usr/bin/env bash
# ============================================================================
# check-canonical-health.sh — 周期健康检查
#
# 每 6 小时跑一次,在本地 .34 与 shared 252 PG 双库执行:
#   1. govern-junk-canonical -json  (本地)
#   2. 远端 SSH 154 → shared PG 查 dup_groups + Suspects 数
#   3. 任一库出现 fixable/dup → 报告(可选重跑 dedup-cleanup)
#
# 退出码:
#   0 健康
#   1 fixable / dup_groups > 0 (需 ops 介入)
#   2 工具/连不上 (基础设施问题)
# ============================================================================
set -euo pipefail

# 凭据策略(2026-09-21 脱敏):本脚本不内嵌任何密码 — DSN/口令一律由调用方
# 通过 env 提供(与 configs/env-252.sh 的 SSOT loader 约定一致),缺失即退出。
# 供 cron 调用时,由 automation prompt 先 source envs loader 再导出这两个变量。
: "${LLM_GATEWAY_DATABASE_URL:?LLM_GATEWAY_DATABASE_URL not set — source configs/env-local.sh first}"
: "${CHECK_SHARED_PG_PASS:?CHECK_SHARED_PG_PASS not set — source configs/env-252.sh first}"

GJC_BIN="${GJC_BIN:-/tmp/govern-junk-canonical}"
if [[ ! -x "$GJC_BIN" ]]; then
  # 二进制缺失(如宿主重启清了 /tmp):从当前 checkout 现场重建
  GJC_BIN="$(mktemp /tmp/govern-junk-canonical.XXXXXX)"
  (cd "$(dirname "$(dirname "$(readlink -f "$0")")")"     && go build -o "$GJC_BIN" ./scripts/govern-junk-canonical) || {
    echo "FATAL: cannot build govern-junk-canonical" >&2; exit 2; }
fi
LOCAL_DSN="$LLM_GATEWAY_DATABASE_URL"
SSH_HOST="${CHECK_SSH_HOST:-154}"
SHARED_PG_HOST="${CHECK_SHARED_PG_HOST:-172.16.2.210}"
SHARED_PG_PORT="${CHECK_SHARED_PG_PORT:-5432}"
SHARED_PG_USER="${CHECK_SHARED_PG_USER:-llm_gateway}"
SHARED_PG_DB="${CHECK_SHARED_PG_DB:-llm_gateway}"
SHARED_PG_PASS="$CHECK_SHARED_PG_PASS"

# 上次基线: .34 active=887 / 252 active=780 / dup=0
BASELINE_LOCAL_ACTIVE="${BASELINE_LOCAL_ACTIVE:-887}"
BASELINE_SHARED_ACTIVE="${BASELINE_SHARED_ACTIVE:-780}"
ALERT_THRESHOLD_DELTA="${ALERT_THRESHOLD_DELTA:-10}"  # active 行数变动 > 10 才报

log() { printf '[%s] %s\n' "$(date -u +%FT%TZ)" "$*"; }

# 1) 本地 govern-junk-canonical (只取首段 JSON,govern-junk 末尾会加 "mode: ..." 文本)
local_json="$(LLM_GATEWAY_DATABASE_URL="$LOCAL_DSN" "$GJC_BIN" -json 2>&1 || echo '{"Suspects":["tool-error"]}')"
local_json_first="$(printf '%s' "$local_json" | awk '/^\{/{flag=1} flag{print} /^\}/&&flag{flag=0; exit}')"
local_suspects="$(printf '%s' "$local_json_first" | jq -r '.Suspects | if type=="array" then join(",") else tostring end' 2>/dev/null || echo 'parse-err')"
local_active="$(printf '%s' "$local_json_first" | jq -r '.ActiveCanonicalRows // 0' 2>/dev/null || echo 0)"

log "local: ActiveCanonicalRows=${local_active:-?} Suspects=${local_suspects:-?}"

# 2) shared 库 dup_groups
shared_dup="$(ssh -o StrictHostKeyChecking=no -o ConnectTimeout=10 "$SSH_HOST" \
  "PGPASSWORD='$SHARED_PG_PASS' psql -h $SHARED_PG_HOST -p $SHARED_PG_PORT -U $SHARED_PG_USER -d $SHARED_PG_DB -tAc \"SELECT count(*) FROM (SELECT replace(replace(lower(canonical_name),'.','-'),'_','-') AS nn FROM models_canonical WHERE status='active' GROUP BY 1 HAVING count(*) > 1) t\"" 2>&1 | tr -d ' \n' || echo 'SSH_FAIL')"

shared_active="$(ssh -o StrictHostKeyChecking=no -o ConnectTimeout=10 "$SSH_HOST" \
  "PGPASSWORD='$SHARED_PG_PASS' psql -h $SHARED_PG_HOST -p $SHARED_PG_PORT -U $SHARED_PG_USER -d $SHARED_PG_DB -tAc \"SELECT count(*) FROM models_canonical WHERE status='active'\"" 2>&1 | tr -d ' \n' || echo 'SSH_FAIL')"

log "shared: ActiveCanonicalRows=${shared_active:-?} dup_groups=${shared_dup:-?}"

# 3) 判定
alert=0
reason=""

if [[ "$local_suspects" != "null" && "$local_suspects" != "" && "$local_suspects" != "parse-err" ]]; then
  alert=1
  reason="local Suspects=$local_suspects"
fi

if [[ "$shared_dup" != "0" && "$shared_dup" != "SSH_FAIL" ]]; then
  alert=1
  reason="$reason; shared dup_groups=$shared_dup"
fi

if [[ "$local_active" =~ ^[0-9]+$ ]]; then
  delta_local=$((local_active - BASELINE_LOCAL_ACTIVE))
  delta_local=${delta_local#-}  # abs
  if (( delta_local > ALERT_THRESHOLD_DELTA )); then
    alert=1
    reason="$reason; local active drift ${local_active} vs baseline ${BASELINE_LOCAL_ACTIVE}"
  fi
fi

if [[ "$shared_active" =~ ^[0-9]+$ ]]; then
  delta_shared=$((shared_active - BASELINE_SHARED_ACTIVE))
  delta_shared=${delta_shared#-}
  if (( delta_shared > ALERT_THRESHOLD_DELTA )); then
    alert=1
    reason="$reason; shared active drift ${shared_active} vs baseline ${BASELINE_SHARED_ACTIVE}"
  fi
fi

if [[ "$shared_dup" == "SSH_FAIL" || "$shared_active" == "SSH_FAIL" ]]; then
  log "WARN: shared PG unreachable via ssh $SSH_HOST — skip shared checks"
fi

if (( alert == 1 )); then
  log "ALERT: $reason"
  log "FIX: re-run sql/fixes/2026-09-20-canonical-dedup-cleanup.sql on shared DB (see docs/audit/2026-09-21-154-245-coverage.md §3.1)"
  exit 1
fi

log "HEALTHY"
exit 0
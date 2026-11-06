#!/usr/bin/env bash
# scripts/audit/probe-schedule-audit.sh — 凭据探测记录小时审计（2026-11-07 探测时间点规划轮的运维延伸）
#
# 目标契约（与 docs/audit/2026-11-07-凭据探测时间点规划与恢复及时性轮.md §4 时间点矩阵对齐）：
#   检查「状态更新延迟」（凭据已恢复但状态未按时更正）与「探测缺失」（该探的行没被探），
#   与上次运行快照**对比**（增量方向比绝对值重要），并对可在系统语义内修复的项做
#   **有界修复**（--fix）：每条修复 SQL 与系统自身组件（credential_recovery 30s tick /
#   write-through / RearmCredentialFailures）同谓词、幂等，绝不引入新语义。
#
# 检查类：
#   R1 配额状态更新延迟   periodic_exhausted 且 recover_at 已过 15min 未被 30s tick 清除
#   R2 可用性状态更新延迟 cooling/rate_limited/unreachable/auth_failed 且 recover_at 已过 5min 未恢复
#   R3 探测缺失（到期未探）错误证据行 next_retry_at 超期 15min 且 last_attempt_at 更早
#      —— 泵饿死/门拦截/ Eligibility 拦截的统状信号
#   R4 探测成功但状态未更正 last_direct_ok=TRUE 而绑定仍 available=FALSE（write-through 缺口）
#   R5 恢复后未复检       凭据已 ok/ready 但失败行仍停在未来 >30min 且 30min 内没探过（F5 回拉缺口）
#   M0 队列积压趋势       credential_probe_queue ready 且到期行数（对比用，只报告）
#
# 用法:
#   bash scripts/audit/probe-schedule-audit.sh <local|245|211> [--fix]
# 退出码: 0=无发现  1=有发现（明细见输出）  2=脚本/连接错误
# 状态与日志: $HOME/.llm-gateway-go/audit/probe-audit-<target>.state / .log
#
# ★ 修复面纪律（与「两把尺」反模式对立）：本脚本只重放系统自己**已经会做**的幂等
#   修复（同一 WHERE），不做任何新语义决策；修不了的（如 R3 持续增长=泵/门缺陷）
#   只报告并登记，交由开发轮根修。
set -uo pipefail

TARGET="${1:-}"
FIX=0
[[ "${2:-}" == "--fix" ]] && FIX=1
if [[ -z "$TARGET" ]] || [[ ! "$TARGET" =~ ^(local|245|211)$ ]]; then
  echo "usage: $0 <local|245|211> [--fix]" >&2
  exit 2
fi

AUDIT_DIR="$HOME/.llm-gateway-go/audit"
mkdir -p "$AUDIT_DIR"
STATE_FILE="$AUDIT_DIR/probe-audit-$TARGET.state"
LOG_FILE="$AUDIT_DIR/probe-audit-$TARGET.log"

# ── 目标连接：SQL 一律经 stdin 进 psql（ssh/docker 管道都不吃长 argv）──────────
case "$TARGET" in
  local)
    psql_stdin() { docker exec -i llm-gateway-pg psql -tA -U llm_gateway -d llm_gateway -v ON_ERROR_STOP=1 2>&1; }
    ;;
  211)
    psql_stdin() { ssh 211 'docker exec -i acc-pg17 psql -tA -U llm_gateway -d llm_gateway -v ON_ERROR_STOP=1' 2>&1; }
    ;;
  245)
    # 245 的网关库在 172.16.2.210（共库），凭据在 245 的 env 文件里；用容器版 psql 过网。
    psql_stdin() {
      ssh 245 'set -a; . /opt/llm-gateway-go/.env >/dev/null 2>&1; set +a; docker run --rm -i -e PGURL="$LLM_GATEWAY_DATABASE_URL" postgres:17-alpine sh -c '\''psql "$PGURL" -tA -v ON_ERROR_STOP=1'\''' 2>&1
    }
    ;;
esac

run_sql() { printf '%s' "$1" | psql_stdin; }

# ── 检查 SQL（全部只读；输出 TSV：首列 = 计数/键）────────────────────────────
SQL_R1="SELECT id, quota_state, extract(epoch FROM now()-quota_recover_at)::int FROM credentials WHERE quota_state='periodic_exhausted' AND quota_recover_at IS NOT NULL AND quota_recover_at < now() - interval '15 minutes' AND lifecycle_status='active' ORDER BY 3 DESC LIMIT 20;"
SQL_R2="SELECT id, availability_state, extract(epoch FROM now()-availability_recover_at)::int FROM credentials WHERE availability_state IN ('cooling','rate_limited','unreachable','auth_failed') AND availability_recover_at IS NOT NULL AND availability_recover_at < now() - interval '5 minutes' AND lifecycle_status='active' ORDER BY 3 DESC LIMIT 20;"
# R3 谓词与泵 pumpDueStatesSQL 对齐（eligibility + binding EXISTS）：
# 手动停用凭据/供应商下的行本来就不该探，不算缺失（2026-11-07 基线实证：67 天
# 停摆行全部是 manual_disabled/lifecycle=disabled 形态＝误报）。
SQL_R3="SELECT credential_id, raw_model_name, extract(epoch FROM now()-next_retry_at)::int AS overdue_s, extract(epoch FROM now()-COALESCE(last_attempt_at,'epoch'))::int AS since_attempt_s, consecutive_failures, COALESCE(last_err_code,'') FROM node_probe_state nps WHERE nps.paused = FALSE AND nps.next_retry_at <= now() - interval '15 minutes' AND COALESCE(nps.last_err_code,'') <> '' AND (nps.last_attempt_at IS NULL OR nps.last_attempt_at < nps.next_retry_at - interval '10 minutes') AND EXISTS (SELECT 1 FROM credentials c JOIN providers p ON p.id=c.provider_id WHERE c.id=nps.credential_id AND COALESCE(c.status,'active')='active' AND COALESCE(c.lifecycle_status,'active')='active' AND COALESCE(c.manual_disabled,FALSE)=FALSE AND COALESCE(p.enabled,FALSE)=TRUE AND COALESCE(p.manual_disabled,FALSE)=FALSE) AND EXISTS (SELECT 1 FROM credential_model_bindings cmb JOIN provider_models pm ON pm.id=cmb.provider_model_id WHERE cmb.credential_id=nps.credential_id AND pm.raw_model_name=nps.raw_model_name) ORDER BY 3 DESC LIMIT 20;"
# R4 收窄（2026-11-07 基线实证两类误报）：① 业务流量在成功探针**之后**又把
# 绑定拉黑（auto_credential_rate_limit）＝正常抖动，只有「拉黑早于成功探针」
# 才是 write-through 缺口；② auto_discovery_expired 等属 discovery 域语义，
# 与 v2 RestoreAllBindings 的豁免面（manual/broken/not_found/deprecated）对齐；
# ③ 停用凭据不在探针服务范围（同 R3 eligibility）。
SQL_R4="SELECT nps.credential_id, nps.raw_model_name, COALESCE(cmb.unavailable_reason,''), extract(epoch FROM now()-nps.last_attempt_at)::int FROM node_probe_state nps JOIN credential_model_bindings cmb ON cmb.credential_id = nps.credential_id JOIN provider_models pm ON pm.id = cmb.provider_model_id AND pm.raw_model_name = nps.raw_model_name WHERE nps.last_direct_ok = TRUE AND COALESCE(cmb.available,FALSE) = FALSE AND COALESCE(cmb.unavailable_reason,'') NOT LIKE 'manual%' AND COALESCE(cmb.unavailable_reason,'') NOT IN ('auto_discovery_expired','auto_model_deprecated','auto_model_not_found','model_probe_broken') AND COALESCE(cmb.admin_protected,FALSE) = FALSE AND COALESCE(cmb.unavailable_at,'epoch') <= nps.last_attempt_at AND EXISTS (SELECT 1 FROM credentials c JOIN providers p ON p.id=c.provider_id WHERE c.id=nps.credential_id AND COALESCE(c.status,'active')='active' AND COALESCE(c.lifecycle_status,'active')='active' AND COALESCE(c.manual_disabled,FALSE)=FALSE AND COALESCE(p.enabled,FALSE)=TRUE AND COALESCE(p.manual_disabled,FALSE)=FALSE) LIMIT 20;"
# R5 收窄（2026-11-07 实证）：http_404/410/协议确认后的 6h 长停放＝合法裁决
# （绑定该降、梯子会在 6h 复检），不算「恢复未复检」。排除面与 clamp 的永久类
# 对齐；剩下的才是真信号（可恢复类行停远期 + 凭据已恢复 = 事件回拉缺口）。
SQL_R5="SELECT nps.credential_id, nps.raw_model_name, extract(epoch FROM nps.next_retry_at-now())::int AS parked_future_s, c.quota_state, COALESCE(c.availability_state,'') FROM node_probe_state nps JOIN credentials c ON c.id = nps.credential_id WHERE c.quota_state='ok' AND COALESCE(c.availability_state,'ready')='ready' AND nps.last_direct_ok IS DISTINCT FROM TRUE AND nps.paused = FALSE AND nps.next_retry_at > now() + interval '30 minutes' AND (nps.last_attempt_at IS NULL OR nps.last_attempt_at < now() - interval '30 minutes') AND lower(COALESCE(nps.last_err_code,'')) NOT IN ('http_404','http_410','model_not_served','probe_http_404','endpoint_build','request_build','decrypt_failed') AND lower(COALESCE(nps.last_err_code,'')) NOT LIKE '%404%' AND lower(COALESCE(nps.last_err_code,'')) NOT LIKE '%410%' AND EXISTS (SELECT 1 FROM credential_model_bindings cmb JOIN provider_models pm ON pm.id=cmb.provider_model_id WHERE cmb.credential_id=nps.credential_id AND pm.raw_model_name=nps.raw_model_name AND COALESCE(cmb.available,FALSE)=FALSE) AND EXISTS (SELECT 1 FROM providers p WHERE p.id = c.provider_id AND COALESCE(p.enabled,FALSE)=TRUE AND COALESCE(p.manual_disabled,FALSE)=FALSE) ORDER BY 3 DESC LIMIT 20;"
SQL_M0="SELECT count(*) FROM credential_probe_queue WHERE status='ready' AND next_run_at <= now();"

# ── 修复 SQL（--fix；与系统自身组件同谓词、幂等、带超时与行数上限）────────────
FIX_R1="BEGIN; SET LOCAL statement_timeout='10s'; UPDATE credentials SET quota_state='ok', quota_recover_at=NULL, state_updated_at=now() WHERE quota_state='periodic_exhausted' AND quota_recover_at IS NOT NULL AND quota_recover_at <= now() AND lifecycle_status='active'; COMMIT;"
FIX_R2="BEGIN; SET LOCAL statement_timeout='10s'; UPDATE credentials SET availability_state='ready', availability_recover_at=NULL, state_updated_at=now() WHERE availability_state IN ('cooling','rate_limited','unreachable','auth_failed') AND availability_recover_at IS NOT NULL AND availability_recover_at <= now() AND lifecycle_status='active' AND (availability_state <> 'suspended' OR COALESCE(quota_state,'ok') NOT IN ('permanently_exhausted','balance_exhausted')); COMMIT;"
FIX_R3="BEGIN; SET LOCAL statement_timeout='10s'; UPDATE node_probe_state s SET next_retry_at = now(), updated_at = now() WHERE s.ctid IN (SELECT ctid FROM node_probe_state WHERE paused = FALSE AND next_retry_at <= now() - interval '15 minutes' AND COALESCE(last_err_code,'') <> '' AND (last_attempt_at IS NULL OR last_attempt_at < next_retry_at - interval '10 minutes') LIMIT 50); COMMIT;"
FIX_R4="BEGIN; SET LOCAL statement_timeout='10s'; UPDATE credential_model_bindings cmb SET available = TRUE, unavailable_reason = NULL, unavailable_at = NULL, unavailable_recover_at = NULL, updated_at = now() WHERE cmb.ctid IN (SELECT cmb.ctid FROM node_probe_state nps JOIN credential_model_bindings cmb ON cmb.credential_id = nps.credential_id JOIN provider_models pm ON pm.id = cmb.provider_model_id AND pm.raw_model_name = nps.raw_model_name WHERE nps.last_direct_ok = TRUE AND COALESCE(cmb.available,FALSE) = FALSE AND COALESCE(cmb.unavailable_reason,'') NOT LIKE 'manual%' AND COALESCE(cmb.unavailable_reason,'') NOT IN ('auto_discovery_expired','auto_model_deprecated','auto_model_not_found','model_probe_broken') AND COALESCE(cmb.admin_protected,FALSE) = FALSE AND COALESCE(cmb.unavailable_at,'epoch') <= nps.last_attempt_at AND EXISTS (SELECT 1 FROM credentials c JOIN providers p ON p.id=c.provider_id WHERE c.id=nps.credential_id AND COALESCE(c.status,'active')='active' AND COALESCE(c.lifecycle_status,'active')='active' AND COALESCE(c.manual_disabled,FALSE)=FALSE AND COALESCE(p.enabled,FALSE)=TRUE AND COALESCE(p.manual_disabled,FALSE)=FALSE) LIMIT 50); COMMIT;"
FIX_R5="BEGIN; SET LOCAL statement_timeout='10s'; UPDATE node_probe_state s SET next_retry_at = now(), updated_at = now() WHERE s.ctid IN (SELECT nps.ctid FROM node_probe_state nps JOIN credentials c ON c.id = nps.credential_id WHERE c.quota_state='ok' AND COALESCE(c.availability_state,'ready')='ready' AND nps.last_direct_ok IS DISTINCT FROM TRUE AND nps.paused = FALSE AND nps.next_retry_at > now() + interval '30 minutes' AND (nps.last_attempt_at IS NULL OR nps.last_attempt_at < now() - interval '30 minutes') AND lower(COALESCE(nps.last_err_code,'')) NOT IN ('http_404','http_410','model_not_served','probe_http_404','endpoint_build','request_build','decrypt_failed') AND lower(COALESCE(nps.last_err_code,'')) NOT LIKE '%404%' AND lower(COALESCE(nps.last_err_code,'')) NOT LIKE '%410%' AND EXISTS (SELECT 1 FROM providers p WHERE p.id = c.provider_id AND COALESCE(p.enabled,FALSE)=TRUE AND COALESCE(p.manual_disabled,FALSE)=FALSE) LIMIT 50); COMMIT;"

echo "== probe-schedule-audit target=$TARGET fix=$FIX $(date '+%F %T') =="

count_rows() { # -tA 输出行数；空输出=0 行（合法）；含 ERROR/FATAL = 查询失败(-1)
  local out=$1
  if [[ "$out" == *ERROR* || "$out" == *FATAL* ]]; then echo -1
  elif [[ -z "$out" ]]; then echo 0
  else printf '%s\n' "$out" | grep -c .
  fi
}

# R1
R1=$(run_sql "$SQL_R1"); R1N=$(count_rows "$R1")
echo "-- R1 配额状态更新延迟(>15min): ${R1N#-} 行"; [[ "$R1" != "" && "$R1" != *ERROR* ]] && printf '%s\n' "$R1" | sed 's/^/     /'
# R2
R2=$(run_sql "$SQL_R2"); R2N=$(count_rows "$R2")
echo "-- R2 可用性状态更新延迟(>5min): $R2N 行"; [[ "$R2" != "" && "$R2" != *ERROR* ]] && printf '%s\n' "$R2" | sed 's/^/     /'
# R3
R3=$(run_sql "$SQL_R3"); R3N=$(count_rows "$R3")
echo "-- R3 探测缺失(到期>15min 未探): $R3N 行"; [[ "$R3" != "" && "$R3" != *ERROR* ]] && printf '%s\n' "$R3" | sed 's/^/     /'
# R4
R4=$(run_sql "$SQL_R4"); R4N=$(count_rows "$R4")
echo "-- R4 探测成功但绑定未恢复: $R4N 行"; [[ "$R4" != "" && "$R4" != *ERROR* ]] && printf '%s\n' "$R4" | sed 's/^/     /'
# R5
R5=$(run_sql "$SQL_R5"); R5N=$(count_rows "$R5")
echo "-- R5 恢复后未复检(停未来>30min): $R5N 行"; [[ "$R5" != "" && "$R5" != *ERROR* ]] && printf '%s\n' "$R5" | sed 's/^/     /'
# M0
M0=$(run_sql "$SQL_M0" | tr -d '[:space:]')
echo "-- M0 队列到期积压(ready & due): ${M0:-?} 行"

if [[ "$R1N" == "-1" || "$R2N" == "-1" || "$R3N" == "-1" || "$R4N" == "-1" || "$R5N" == "-1" || -z "$M0" ]]; then
  echo "!! 查询失败（连接/权限），本次审计无效"
  exit 2
fi

# ── 与上次快照对比 ──────────────────────────────────────────────────────────
if [[ -f "$STATE_FILE" ]]; then
  pts=""; pR1=""; pR2=""; pR3=""; pR4=""; pR5=""; pM0=""
  while IFS='=' read -r k v; do
    case "$k" in
      TS) pts=$v ;; R1) pR1=$v ;; R2) pR2=$v ;; R3) pR3=$v ;;
      R4) pR4=$v ;; R5) pR5=$v ;; M0) pM0=$v ;;
    esac
  done < "$STATE_FILE"
  echo "-- 对比（上次 ${pts:-?} → 本次）: R1 ${pR1:-?}→$R1N  R2 ${pR2:-?}→$R2N  R3 ${pR3:-?}→$R3N  R4 ${pR4:-?}→$R4N  R5 ${pR5:-?}→$R5N  M0 ${pM0:-?}→$M0"
else
  echo "-- 对比: 无上次快照（首跑建基线）"
fi

# ── 有界修复（--fix）────────────────────────────────────────────────────────
FIXED=0
if (( FIX )); then
  if (( R1N > 0 )); then out=$(run_sql "$FIX_R1"); if [[ "$out" == *ERROR* ]]; then echo "FIX R1 失败: $(printf '%s' "$out" | head -2)"; else n=$(printf '%s' "$out" | sed -n 's/^UPDATE \([0-9]*\)$/\1/p' | tail -1); echo "FIX R1 重放 quota 恢复: ${n:-0} 行"; FIXED=$((FIXED + ${n:-0})); fi; fi
  if (( R2N > 0 )); then out=$(run_sql "$FIX_R2"); if [[ "$out" == *ERROR* ]]; then echo "FIX R2 失败: $(printf '%s' "$out" | head -2)"; else n=$(printf '%s' "$out" | sed -n 's/^UPDATE \([0-9]*\)$/\1/p' | tail -1); echo "FIX R2 重放 availability 恢复: ${n:-0} 行"; FIXED=$((FIXED + ${n:-0})); fi; fi
  if (( R3N > 0 )); then out=$(run_sql "$FIX_R3"); if [[ "$out" == *ERROR* ]]; then echo "FIX R3 失败: $(printf '%s' "$out" | head -2)"; else n=$(printf '%s' "$out" | sed -n 's/^UPDATE \([0-9]*\)$/\1/p' | tail -1); echo "FIX R3 到期未探行 re-arm: ${n:-0} 行"; FIXED=$((FIXED + ${n:-0})); fi; fi
  if (( R4N > 0 )); then out=$(run_sql "$FIX_R4"); if [[ "$out" == *ERROR* ]]; then echo "FIX R4 失败: $(printf '%s' "$out" | head -2)"; else n=$(printf '%s' "$out" | sed -n 's/^UPDATE \([0-9]*\)$/\1/p' | tail -1); echo "FIX R4 write-through 补写绑定: ${n:-0} 行"; FIXED=$((FIXED + ${n:-0})); fi; fi
  if (( R5N > 0 )); then out=$(run_sql "$FIX_R5"); if [[ "$out" == *ERROR* ]]; then echo "FIX R5 失败: $(printf '%s' "$out" | head -2)"; else n=$(printf '%s' "$out" | sed -n 's/^UPDATE \([0-9]*\)$/\1/p' | tail -1); echo "FIX R5 恢复行 re-arm: ${n:-0} 行"; FIXED=$((FIXED + ${n:-0})); fi; fi
fi

# ── 快照落盘 + 日志追加 ─────────────────────────────────────────────────────
ts=$(date '+%F %T')
cat > "$STATE_FILE" << EOF
TS=$ts
R1=$R1N
R2=$R2N
R3=$R3N
R4=$R4N
R5=$R5N
M0=$M0
EOF
{ echo "== $ts target=$TARGET fix=$FIX R1=$R1N R2=$R2N R3=$R3N R4=$R4N R5=$R5N M0=$M0 FIXED=${FIXED:-0}"; } >> "$LOG_FILE"

TOTAL=$((R1N + R2N + R3N + R4N + R5N))
echo "== 审计完成: findings=$TOTAL fixed=${FIXED:-0} =="

# R3/R4/R5 是机制性信号：出现即应在开发轮根修（本脚本只治标）
if (( R4N > 0 )); then echo "!! R4 属 write-through 机制缺口信号，需开发轮排查（本审计已按系统同谓词补写）"; fi
if (( R3N > 0 )); then echo "!! R3 若连续多轮增长 = 泵饿死/门拦截缺陷信号，需开发轮排查"; fi
if (( R5N > 0 )); then echo "!! R5 若连续多轮出现 = F5 事件回拉缺口信号，需开发轮排查"; fi

if (( TOTAL > 0 )); then exit 1; fi
exit 0

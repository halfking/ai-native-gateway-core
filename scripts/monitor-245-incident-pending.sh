#!/usr/bin/env bash
# =====================================================================
# scripts/monitor-245-incident-pending.sh — 715 pending 状态部署后监控
#
# 用途: bba08b922 + 6f3d03073（715 迁移接线）部署到 245 后的前 24 小时
#       观察。每 2-4 小时跑一次（手动或 crontab），输出 markdown 报告
#       到 .handoff/，供逐 checkpoint 对比四项验收指标：
#
#         M1 事件可见性   pending vs active（预期 ≈ 2:1，稳态失败混合下）
#         M2 凭证恢复率   冷却后 available=true 占比（预期 > 95%）
#         M3 误报率       streak<3 即自愈（从未可见）的事件占比，
#                         与部署前基线（事件报告 09-16）对比应降 60%+
#         M4 迁移就绪     schema_migrations '715' + CHECK 含 pending
#                         + journald 无 23514（部署顺序异常的回归信号）
#
# 用法:
#   export LLM_GATEWAY_245_DB_PASSWORD=...   # 与 monitor-245-checkpoint.sh 相同
#   bash scripts/monitor-245-incident-pending.sh [checkpoint-label]
#
# 阈值行为备注: failure_to_active 当前硬编码为 DefaultThresholds()=3
# （store.go DecideState 调用点），settings 表不可调——出现"阈值行为
# 异常"时先核对代码版本，不要去找 settings.route_incidents（不存在）。
# =====================================================================
set -euo pipefail

CHECKPOINT_NUM=${1:-$(date +"%Y%m%d-%H%M")}
[[ "$CHECKPOINT_NUM" =~ ^[A-Za-z0-9._-]+$ ]] || {
  echo "checkpoint 标识只能包含字母、数字、点、下划线和连字符" >&2
  exit 2
}

: "${LLM_GATEWAY_245_DB_PASSWORD:?LLM_GATEWAY_245_DB_PASSWORD must be set for database checks}"

SSH_HOST="${LLM_GATEWAY_245_SSH_HOST:-root@8.136.114.245}"
SSH_PORT="${LLM_GATEWAY_245_SSH_PORT:-25022}"
DB_HOST="${LLM_GATEWAY_245_DB_HOST:-172.16.2.210}"
DB_PORT="${LLM_GATEWAY_245_DB_PORT:-5432}"
DB_USER="${LLM_GATEWAY_245_DB_USER:-llm_gateway}"
DB_NAME="${LLM_GATEWAY_245_DB_NAME:-llm_gateway}"
SERVICE_NAME="${LLM_GATEWAY_245_SERVICE_NAME:-llmgo-245.service}"
REPORT_DIR="${LLM_GATEWAY_245_REPORT_DIR:-$PWD/.handoff}"
TIMESTAMP=$(date +"%Y-%m-%d %H:%M:%S")
REPORT_FILE="${REPORT_DIR}/incident-pending-${CHECKPOINT_NUM}.md"
SSH_OPTS=(-o BatchMode=yes -o StrictHostKeyChecking=accept-new -p "$SSH_PORT")
if [[ -n "${LLM_GATEWAY_245_SSH_IDENTITY_FILE:-}" ]]; then
  SSH_OPTS+=(-i "$LLM_GATEWAY_245_SSH_IDENTITY_FILE")
fi

remote_ssh() {
  ssh "${SSH_OPTS[@]}" "$SSH_HOST" "$@"
}

remote_psql() {
  local query=$1
  printf '%s\n%s\n' "$LLM_GATEWAY_245_DB_PASSWORD" "$query" |
    ssh "${SSH_OPTS[@]}" "$SSH_HOST" '
      IFS= read -r PGPASSWORD
      IFS= read -r QUERY
      export PGPASSWORD
      exec psql -X -v ON_ERROR_STOP=1 -h "'"$DB_HOST"'" -p "'"$DB_PORT"'" -U "'"$DB_USER"'" -d "'"$DB_NAME"'" -t -A -c "$QUERY"
    '
}

mkdir -p "$REPORT_DIR"

# 报告同时落盘与输出；单项查询失败不中断整体（该项标记失败原因）。
end_section() {
  echo '```' >>"$REPORT_FILE"
}

# 远端 helper 的 QUERY 走单行 read，多行 SQL 必须先压平成单行。
run_sql_section() {
  # run_sql_section "标题" <<'SQL' ... SQL   —— heredoc 传查询
  local title=$1
  local query
  query=$(cat | tr '\n' ' ' | sed 's/[[:space:]]\+/ /g; s/^ //; s/ $//')
  {
    echo ""
    echo "## ${title}"
    echo '```'
  } >>"$REPORT_FILE"
  echo -e "\n== ${title} ==" >&2
  if ! remote_psql "$query" | tee -a "$REPORT_FILE"; then
    echo "（查询失败，见上方 psql 报错）" | tee -a "$REPORT_FILE"
  fi
  end_section
}

{
  echo "# 715 pending 状态部署监控 — checkpoint ${CHECKPOINT_NUM}"
  echo ""
  echo "- 时间: ${TIMESTAMP}"
  echo "- 目标: ${SERVICE_NAME} @ ${SSH_HOST}:${SSH_PORT} / db ${DB_USER}@${DB_HOST}:${DB_PORT}/${DB_NAME}"
} >"$REPORT_FILE"

# ── 0. 服务健康与版本 ────────────────────────────────────────────────
{
  echo ""
  echo "## 0. 服务健康"
  echo '```'
} >>"$REPORT_FILE"
SERVICE_STATUS=$(remote_ssh "systemctl is-active '$SERVICE_NAME'" 2>/dev/null || echo inactive)
SERVICE_UPTIME=$(remote_ssh "systemctl status '$SERVICE_NAME' --no-pager 2>/dev/null | grep -m1 'Active:'" || true)
SERVICE_VERSION=$(remote_ssh "curl -fsS http://127.0.0.1:8781/healthz 2>/dev/null | head -c 400" || echo "healthz 不可达")
{
  echo "is-active: ${SERVICE_STATUS}"
  echo "${SERVICE_UPTIME}"
  echo "healthz: ${SERVICE_VERSION}"
} | tee -a "$REPORT_FILE"
end_section
if [[ "$SERVICE_STATUS" != "active" ]]; then
  echo "⛔ 服务非 active，后续 DB 指标仅作参考" >&2
fi

# ── M4. 迁移就绪（部署顺序自检，异常处理第 1 条）────────────────────
run_sql_section "M4a. 迁移账本 715 stamp（应返回 1）" <<'SQL'
SELECT COALESCE((SELECT 1 FROM schema_migrations WHERE version = '715'), 0);
SQL

run_sql_section "M4b. route_incidents CHECK 约束形态（应含 pending）" <<'SQL'
SELECT pg_get_constraintdef(oid) FROM pg_constraint
WHERE conname = 'route_incidents_state_check'
  AND conrelid = 'route_incidents'::regclass;
SQL

run_sql_section "M4c. 唯一索引谓词（WHERE 应含 pending）" <<'SQL'
SELECT pg_get_expr(i.indpred, i.indrelid)
FROM pg_index i JOIN pg_class c ON c.oid = i.indexrelid
WHERE c.relname = 'uq_route_incidents_active_route';
SQL

# 23514 = 约束未就绪时新代码写入 pending 的标志性错误（回归信号）。
{
  echo ""
  echo "## M4d. journald 近 4 小时 23514 / routeincident 错误（应均为 0）"
  echo '```'
} >>"$REPORT_FILE"
journal_errors=$(remote_ssh "journalctl -u '$SERVICE_NAME' --since '4 hours ago' --no-pager 2>/dev/null | grep -c -E '23514|check constraint' || true" || echo "journalctl 不可用")
gaveup_errors=$(remote_ssh "journalctl -u '$SERVICE_NAME' --since '4 hours ago' --no-pager 2>/dev/null | grep -c 'routeincident transition gave up' || true" || echo "journalctl 不可用")
{
  echo "constraint_violations(23514): ${journal_errors}"
  echo "observer_gave_up: ${gaveup_errors}"
} | tee -a "$REPORT_FILE"
end_section

# ── M1. 事件可见性：state 分布（任务验证 SQL #1）────────────────────
run_sql_section "M1. route_incidents state 分布（pending:active 预期 ≈ 2:1）" <<'SQL'
SELECT state, COUNT(*) FROM route_incidents GROUP BY state ORDER BY state;
SQL

# ── 事件转换明细（任务验证 SQL #3）──────────────────────────────────
run_sql_section "转换明细: state × failure_streak" <<'SQL'
SELECT state, failure_streak, COUNT(*)
FROM route_incidents
GROUP BY state, failure_streak
ORDER BY state, failure_streak;
SQL

# ── M3. 误报率代理：从未可见（streak<3 自愈）vs 达到 active ─────────
run_sql_section "M3. 近 24h 自愈事件细分（streak<3 = 部署前会误报的群体）" <<'SQL'
SELECT CASE WHEN failure_streak < 3 THEN 'subthreshold_never_visible' ELSE 'reached_active' END AS cohort,
       COUNT(*) AS incidents,
       ROUND(AVG(total_failures), 2) AS avg_failures
FROM route_incidents
WHERE state IN ('recovered', 'recovering')
  AND COALESCE(recovered_at, updated_at) >= NOW() - INTERVAL '24 hours'
GROUP BY 1 ORDER BY 1;
SQL

# ── M2. 凭证恢复率（任务验证 SQL #2；真实表为 credential_state_log）──
run_sql_section "M2a. 近 1h 有失败的凭证对：available / recover_at 持久化（Bug1 修复证据，available 不应全为空）" <<'SQL'
SELECT credential_id, raw_model_name, available, recover_at, last_failure_at, updated_at
FROM credential_state_log
WHERE last_failure_at > NOW() - INTERVAL '1 hour'
ORDER BY last_failure_at DESC
LIMIT 30;
SQL

run_sql_section "M2b. 近 24h 冷却恢复率（available=true 占比，预期 > 95%）" <<'SQL'
SELECT COUNT(*) AS pairs_with_recent_failure,
       COUNT(*) FILTER (WHERE available = true) AS recovered_available,
       ROUND(100.0 * COUNT(*) FILTER (WHERE available = true) / NULLIF(COUNT(*), 0), 2) AS recovery_rate_pct,
       COUNT(*) FILTER (WHERE available = false AND recover_at IS NOT NULL) AS cooling_with_recover_at,
       COUNT(*) FILTER (WHERE available = false AND recover_at IS NULL) AS cooling_missing_recover_at
FROM credential_state_log
WHERE last_failure_at > NOW() - INTERVAL '24 hours';
SQL

# ── 事件流佐证 ──────────────────────────────────────────────────────
run_sql_section "近 24h 事件流: opened vs failure_observed vs recovered" <<'SQL'
SELECT e.event_type, COUNT(*) AS events
FROM route_incident_events e
WHERE e.created_at >= NOW() - INTERVAL '24 hours'
GROUP BY e.event_type ORDER BY events DESC;
SQL

{
  echo ""
  echo "## 结论模板（人工填写）"
  echo '```'
  echo "M1 pending:active = ___:___ （预期 ≈ 2:1）"
  echo "M2 恢复率 = ___% （预期 > 95%）; cooling_missing_recover_at 应为 0"
  echo "M3 subthreshold_never_visible 占比 = ___%（对比 09-16 基线应降 60%+）"
  echo "M4 stamp=1 / CHECK 含 pending / 23514=0 → 迁移就绪"
  echo '```'
} >>"$REPORT_FILE"

echo ""
echo "========================================="
echo "报告已写入: ${REPORT_FILE}"
echo "========================================="
cat "$REPORT_FILE"

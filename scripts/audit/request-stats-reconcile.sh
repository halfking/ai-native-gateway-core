#!/usr/bin/env bash
# request-stats-reconcile.sh —— 请求统计四腿对账（运营审计轮项1，2026-11-09）
#
# 用法:
#   ./request-stats-reconcile.sh [--hours 24] [--exec "docker exec acc-pg17 psql -U acc_super -d llm_gateway -tAc"]
#
# 四腿：
#   A. client 面        request_logs_with_current_month（看板/前端口径）
#   B. minute rollup    request_stats_minute（+ 近期闭分钟重扫臂，F1 修复后应收敛）
#   C. usage_ledger     usage_ledger_with_current_month（上游计费面；含压测/预日志失败行，差值须可解释）
#   D. WAL              request_wal_hot（生命周期行，status 语义不同，仅参考）
# 另报：
#   · cost_usd 非空行数与总额（0 = 绑定无单价 ⇒ 上游成本链死，登记数据缺口）
#   · B 对 A 的每分钟缺口 top10（F1：迟到终态行曾造成 30-50% 漏统）
#
# 退出码：0 = 无未解释缺口；1 = 有缺口（rollup 缺口>2% 或 A/C 差值无法由已知类解释）。
set -euo pipefail

HOURS=24
PSQL_CMD=${PSQL_CMD:-"docker exec acc-pg17 psql -U acc_super -d llm_gateway -tAc"}
while [ $# -gt 0 ]; do
  case "$1" in
    --hours) HOURS="$2"; shift 2 ;;
    --exec) PSQL_CMD="$2"; shift 2 ;;
    *) echo "unknown arg $1" >&2; exit 64 ;;
  esac
done

q() { $PSQL_CMD "$1"; }

echo "── window: last ${HOURS}h ──"

A_SUCCESS=$(q "SELECT count(*) FROM request_logs_with_current_month WHERE ts >= now() - interval '${HOURS} hours' AND request_status='success'")
A_FAIL=$(q "SELECT count(*) FROM request_logs_with_current_month WHERE ts >= now() - interval '${HOURS} hours' AND request_status='failure'")
A_TOTAL=$((A_SUCCESS + A_FAIL))
A_COST_ROWS=$(q "SELECT count(*) FROM request_logs_with_current_month WHERE ts >= now() - interval '${HOURS} hours' AND cost_usd IS NOT NULL AND cost_usd > 0")
A_PROMPT=$(q "SELECT COALESCE(sum(prompt_tokens),0) FROM request_logs_with_current_month WHERE ts >= now() - interval '${HOURS} hours'")
B_REQ=$(q "SELECT COALESCE(sum(requests),0) FROM request_stats_minute WHERE bucket >= now() - interval '${HOURS} hours'")
B_SUCC=$(q "SELECT COALESCE(sum(success_count),0) FROM request_stats_minute WHERE bucket >= now() - interval '${HOURS} hours'")
C_TOTAL=$(q "SELECT count(*) FROM usage_ledger_with_current_month WHERE ts >= now() - interval '${HOURS} hours'")
C_SUCCESS=$(q "SELECT count(*) FROM usage_ledger_with_current_month WHERE ts >= now() - interval '${HOURS} hours' AND success")
C_COST_ROWS=$(q "SELECT count(*) FROM usage_ledger_with_current_month WHERE ts >= now() - interval '${HOURS} hours' AND cost_usd IS NOT NULL AND cost_usd > 0")
C_ONLY=$(q "SELECT count(*) FROM usage_ledger_with_current_month l WHERE l.ts >= now() - interval '${HOURS} hours' AND NOT EXISTS (SELECT 1 FROM request_logs_with_current_month r WHERE r.request_id = l.request_id)")
WAL_ROWS=$(q "SELECT count(*) FROM request_wal_hot WHERE created_at >= now() - interval '${HOURS} hours'")

echo "A client_logs      : total=${A_TOTAL} success=${A_SUCCESS} fail=${A_FAIL} cost>0rows=${A_COST_ROWS} prompt_tokens=${A_PROMPT}"
echo "B minute_rollup    : requests=${B_REQ} success=${B_SUCC}"
echo "C usage_ledger     : total=${C_TOTAL} success=${C_SUCCESS} cost>0rows=${C_COST_ROWS} ledger_only=${C_ONLY}（须= 压测/预日志失败类）"
echo "D request_wal      : rows=${WAL_ROWS}（状态语义不同，仅参考）"

RC=0
if [ "$A_TOTAL" -gt 0 ]; then
  GAP=$((A_TOTAL - B_REQ))
  GAP_PCT=$((GAP * 100 / A_TOTAL))
  echo "gap B-A            : ${GAP} (${GAP_PCT}%)"
  if [ "$GAP_PCT" -gt 2 ]; then
    echo "GATE: rollup gap > 2% — F1 resweep 未收敛或未部署"
    RC=1
    echo "── top10 per-minute gaps ──"
    q "WITH m AS (SELECT date_trunc('minute', ts) b, count(*) n FROM request_logs_with_current_month WHERE ts >= now() - interval '${HOURS} hours' AND request_status IN ('success','failure') GROUP BY 1), s AS (SELECT bucket, sum(requests) n FROM request_stats_minute WHERE bucket >= now() - interval '${HOURS} hours' GROUP BY 1) SELECT m.b, m.n, COALESCE(s.n,0), m.n - COALESCE(s.n,0) FROM m FULL JOIN s ON s.bucket = m.b ORDER BY gap DESC NULLS LAST LIMIT 10" 2>/dev/null || q "WITH m AS (SELECT date_trunc('minute', ts) b, count(*) n FROM request_logs_with_current_month WHERE ts >= now() - interval '${HOURS} hours' AND request_status IN ('success','failure') GROUP BY 1), s AS (SELECT bucket, sum(requests) n FROM request_stats_minute WHERE bucket >= now() - interval '${HOURS} hours' GROUP BY 1), x AS (SELECT m.b, m.n, COALESCE(s.n,0) rn, m.n - COALESCE(s.n,0) gap FROM m FULL JOIN s ON s.bucket = m.b) SELECT x.b, x.n, x.rn, x.gap FROM x ORDER BY x.gap DESC LIMIT 10"
  fi
fi
if [ "$C_COST_ROWS" = "0" ]; then
  echo "NOTE: 上游成本链死（cost_usd 全 0/NULL）＝ 绑定无单价（数据缺口，见 2026-11-03 模型价格情报轮 + salesdecorate 行情整合）"
fi
if [ "$C_ONLY" -gt 0 ] && [ "$C_ONLY" -gt $((A_TOTAL / 10)) ]; then
  echo "GATE: ledger_only 占比 >10% 且未定性 — 排查 request_id 差集"
  RC=1
fi
exit $RC

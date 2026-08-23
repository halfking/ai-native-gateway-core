#!/usr/bin/env bash
# 245 minimax-m3 request_logs_hot 写入缺失诊断脚本
# 2026-08-23 — 配套新增的 telemetry_sanitize_events_total Prometheus counter
# 与 EmitRequestLogUpdate 必填字段守卫。
#
# 用法（在 245 网关主机上）：
#   sudo ./245-minimax-m3-requestlog-investigation.sh
#
# 前置条件：
#   - 网关日志在 /var/log/llm-gateway/  或可自定义 LOG_DIR
#   - PostgreSQL 客户端（psql）已安装；可通过 PGHOST/PGUSER/PGDATABASE 环境变量覆盖
#
# 输出：
#   - stdout：人工阅读的诊断结论
#   - ./diag-out/ 目录：每个步骤的原始输出，方便后续贴 issue

set -u
set -o pipefail

LOG_DIR="${LOG_DIR:-/var/log/llm-gateway}"
OUT_DIR="${OUT_DIR:-./diag-out}"
PGHOST="${PGHOST:-127.0.0.1}"
PGUSER="${PGUSER:-postgres}"
PGDATABASE="${PGDATABASE:-llm_gateway}"
PGPORT="${PGPORT:-5432}"
LOOKBACK_HOURS="${LOOKBACK_HOURS:-1}"

mkdir -p "$OUT_DIR"

log()  { printf '\033[1;34m[%s]\033[0m %s\n' "$(date +%H:%M:%S)" "$*"; }
warn() { printf '\033[1;33m[WARN]\033[0m %s\n' "$*"; }
fail() { printf '\033[1;31m[FAIL]\033[0m %s\n' "$*"; }
ok()   { printf '\033[1;32m[OK]\033[0m   %s\n'   "$*"; }

echo "================ 245 minimax-m3 request_logs_hot 调查 ================"
echo "日志目录 : $LOG_DIR"
echo "PG       : $PGUSER@$PGHOST:$PGPORT/$PGDATABASE"
echo "回看时长 : 最近 $LOOKBACK_HOURS 小时"
echo

# ----------------------------------------------------------------------------
# 步骤 1：日志里的烟枪
# ----------------------------------------------------------------------------
log "Step 1: grep 网关日志中的 telemetry 失败/丢弃线索"

declare -a SIGNATURES=(
  '"telemetry JSON field discarded, storing NULL"'
  '"telemetry JSONB field discarded"'
  '"telemetry JSON field repaired by truncation"'
  '"telemetry JSONB field repaired by truncation"'
  '"telemetry request sync persist failed"'
  '"telemetry request db persist failed'
  '"telemetry EmitRequestLogUpdate dropped: missing request_id"'
  'emitTelemetry: requestBody empty for known model'
)

for sig in "${SIGNATURES[@]}"; do
  printf "  %-60s" "$sig"
  # 同时收 minimax-m3 与所有模型；后者用来判断是不是 minimax 独有
  if [[ -d "$LOG_DIR" ]]; then
    cnt_minimax=$(grep -rh "$sig" "$LOG_DIR" --include='*.log' 2>/dev/null \
      | grep -ci 'minimax' || true)
    cnt_total=$(grep -rh "$sig" "$LOG_DIR" --include='*.log' 2>/dev/null | wc -l | tr -d ' ')
    echo "minimax=$cnt_minimax  total=$cnt_total"
    grep -rh "$sig" "$LOG_DIR" --include='*.log' 2>/dev/null \
      | tail -50 > "$OUT_DIR/log-$(echo "$sig" | tr -dc '[:alnum:]_').txt" || true
  else
    warn "日志目录不存在：$LOG_DIR"
    echo "n/a"
  fi
done

echo
log "Step 2: 拉取 minimax-m3 最近的 gateway 入口日志（5xx / client_disconnect / stream_interrupted）"

if [[ -d "$LOG_DIR" ]]; then
  grep -rh 'minimax-m3' "$LOG_DIR" --include='*.log' 2>/dev/null \
    | grep -E 'client_disconnect|stream_interrupted|EOF|reset by peer|status=5|status 5' \
    | tail -50 > "$OUT_DIR/log-minimax-error-context.txt"
  cnt=$(wc -l < "$OUT_DIR/log-minimax-error-context.txt" | tr -d ' ')
  echo "  命中行数: $cnt（详情见 $OUT_DIR/log-minimax-error-context.txt）"
else
  warn "日志目录不存在"
fi

# ----------------------------------------------------------------------------
# 步骤 3：DB 对照
# ----------------------------------------------------------------------------
echo
log "Step 3: PostgreSQL 数据对比（WAL vs logs_hot vs 字段空值率）"

if ! command -v psql >/dev/null 2>&1; then
  warn "psql 未安装，跳过 DB 步骤"
else
  export PGPASSWORD="${PGPASSWORD:-}"

  # 3a) 模型维度行数对比
  psql -h "$PGHOST" -p "$PGPORT" -U "$PGUSER" -d "$PGDATABASE" -At -F'|' <<SQL > "$OUT_DIR/sql-model-counts.txt"
SELECT
  model,
  count(*) FILTER (WHERE request_status = 'success')   AS success,
  count(*) FILTER (WHERE request_status = 'failure')   AS failure,
  count(*) FILTER (WHERE request_status = 'client_disconnect') AS client_disc,
  count(*) FILTER (WHERE request_status NOT IN ('success','failure','client_disconnect') OR request_status IS NULL) AS other,
  count(*) AS total
FROM request_logs_hot
WHERE ts > now() - interval '$LOOKBACK_HOURS hour'
  AND model IN ('minimax-m3','gpt-5.6-terra')
GROUP BY model
ORDER BY model;
SQL

  echo "  [3a] 各模型在 request_logs_hot 的状态分布："
  column -t -s'|' "$OUT_DIR/sql-model-counts.txt" | sed 's/^/    /'
  echo

  # 3b) WAL（总写盘）与 logs_hot 的差 = 真丢失
  psql -h "$PGHOST" -p "$PGPORT" -U "$PGUSER" -d "$PGDATABASE" -At -F'|' <<SQL > "$OUT_DIR/sql-wal-vs-logs.txt"
SELECT
  w.model,
  w.wal_rows,
  COALESCE(l.log_rows, 0) AS log_rows,
  (w.wal_rows - COALESCE(l.log_rows, 0)) AS missing_rows
FROM
  (SELECT model, count(*) AS wal_rows
   FROM request_wal_hot
   WHERE ts > now() - interval '$LOOKBACK_HOURS hour'
     AND model IN ('minimax-m3','gpt-5.6-terra')
   GROUP BY model) w
LEFT JOIN
  (SELECT model, count(*) AS log_rows
   FROM request_logs_hot
   WHERE ts > now() - interval '$LOOKBACK_HOURS hour'
     AND model IN ('minimax-m3','gpt-5.6-terra')
   GROUP BY model) l
ON w.model = l.model
ORDER BY w.model;
SQL

  echo "  [3b] WAL 行数 vs request_logs_hot 行数（差额 = 真丢失）："
  column -t -s'|' "$OUT_DIR/sql-wal-vs-logs.txt" | sed 's/^/    /'
  echo

  # 3c) 关键 JSONB 字段空值率
  psql -h "$PGHOST" -p "$PGPORT" -U "$PGUSER" -d "$PGDATABASE" -At -F'|' <<SQL > "$OUT_DIR/sql-null-rates.txt"
SELECT
  model,
  round(100.0 * count(*) FILTER (WHERE request_body     IS NULL) / count(*), 1) AS req_body_null_pct,
  round(100.0 * count(*) FILTER (WHERE response_body    IS NULL) / count(*), 1) AS resp_body_null_pct,
  round(100.0 * count(*) FILTER (WHERE outbound_body    IS NULL) / count(*), 1) AS outbound_null_pct,
  round(100.0 * count(*) FILTER (WHERE routing_attempts IS NULL) / count(*), 1) AS routing_null_pct,
  round(100.0 * count(*) FILTER (WHERE attachments      IS NULL) / count(*), 1) AS attachments_null_pct,
  round(100.0 * count(*) FILTER (WHERE t9_response_end_at IS NULL) / count(*), 1) AS t9_null_pct,
  round(100.0 * count(*) FILTER (WHERE tool_calls       IS NULL) / count(*), 1) AS tool_calls_null_pct,
  round(100.0 * count(*) FILTER (WHERE discard_events   IS NULL) / count(*), 1) AS discard_null_pct
FROM request_logs_hot
WHERE ts > now() - interval '$LOOKBACK_HOURS hour'
  AND model IN ('minimax-m3','gpt-5.6-terra')
GROUP BY model
ORDER BY model;
SQL

  echo "  [3c] 关键字段 NULL 占比（minimax-m3 vs gpt-5.6-terra）："
  column -t -s'|' "$OUT_DIR/sql-null-rates.txt" | sed 's/^/    /'

  # 3d) 失败 + 断开 的明细（带 error_kind）
  psql -h "$PGHOST" -p "$PGPORT" -U "$PGUSER" -d "$PGDATABASE" -At -F'|' <<SQL > "$OUT_DIR/sql-failure-detail.txt"
SELECT
  request_status, error_kind, failure_detail_code, failure_stage, count(*)
FROM request_logs_hot
WHERE ts > now() - interval '$LOOKBACK_HOURS hour'
  AND model = 'minimax-m3'
  AND (request_status <> 'success' OR success = false)
GROUP BY request_status, error_kind, failure_detail_code, failure_stage
ORDER BY count(*) DESC
LIMIT 20;
SQL

  echo
  echo "  [3d] minimax-m3 非成功行的错误分布："
  column -t -s'|' "$OUT_DIR/sql-failure-detail.txt" | sed 's/^/    /'
fi

# ----------------------------------------------------------------------------
# 步骤 4：Prometheus 指标读数（新加的 counter）
# ----------------------------------------------------------------------------
echo
log "Step 4: 拉取 telemetry_sanitize_events_total 指标（如已升级新版本）"

PROM_URL="${PROM_URL:-http://127.0.0.1:9090}"
if command -v curl >/dev/null 2>&1; then
  out=$(curl -sf --max-time 5 \
    "${PROM_URL}/api/v1/query?query=telemetry_sanitize_events_total" 2>/dev/null \
    | head -c 4096 || true)
  if [[ -n "$out" ]]; then
    echo "  $out" | head -c 2000
    echo
  else
    warn "Prometheus 不可达或无此 metric（确认是否已部署新二进制）：$PROM_URL"
  fi
else
  warn "curl 未安装，跳过"
fi

# ----------------------------------------------------------------------------
# 步骤 5：结论
# ----------------------------------------------------------------------------
echo
echo "================ 诊断结论 ================"
echo
echo "1) 若 [3b] 显示 minimax-m3 在 WAL > logs_hot："
echo "   → DB 写入路径有真丢失。检查 client.go:2604 / 2630 的 slog.Warn 与"
echo "     新指标 telemetry_sanitize_events_total{outcome=\"discarded\"}。"
echo
echo "2) 若 [3c] 显示 minimax-m3 在 request_body / outbound_body / t9_… 的"
echo "   NULL 占比远高于 gpt-5.6-terra："
echo "   → 是 sanitizeRequestLogEntry 的 NULL 化（client.go:2535-2588）。"
echo "     JSONB 列被丢弃 = /request-logs UI 看不到完整审计信息。"
echo
echo "3) 若 [3d] 全是 success 但 [3b] 行数对得上："
echo "   → 数据库没问题，前端查询或缓存问题；转 UI 排查。"
echo
echo "4) 若 [3a] minimax-m3 出现 client_disconnect / stream_interrupted："
echo "   → MiniMax API 不发 [DONE] 引起的虚惊失败（已被 c77a7d671 修复）。"
echo "     升级到包含 c77a7d671 的版本可消除此分支。"
echo
echo "原始输出文件位于：$OUT_DIR/"
echo "完成。"

#!/usr/bin/env bash
# 245 minimax-m3 request_logs_hot 写入缺失诊断脚本
# 2026-08-23 — Step 6: 配套 telemetry_sanitize_events_total{outcome=discarded|rescued}
# Prometheus counter、EmitRequestLogUpdate 必填字段守卫、sanitize 后保留 truncated
# prefix 的新行为。
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
# 2026-08-23 (audit P1-3): 默认值改为 ops 标准主机地址。245 网关主机通常
# 不是 PG / Prometheus 的本地宿主机，127.0.0.1 会让 Step 3 / Step 4
# 静默无输出。运维如需直连本地可显式 export PGHOST=127.0.0.1
# PROM_URL=http://127.0.0.1:9090 覆盖。
PGHOST="${PGHOST:-pg-primary.ops.internal}"
PGUSER="${PGUSER:-postgres}"
PGDATABASE="${PGDATABASE:-llm_gateway}"
PGPORT="${PGPORT:-5432}"
LOOKBACK_HOURS="${LOOKBACK_HOURS:-1}"
PROM_URL="${PROM_URL:-http://prom.ops.internal:9090}"

mkdir -p "$OUT_DIR"

log()  { printf '\033[1;34m[%s]\033[0m %s\n' "$(date +%H:%M:%S)" "$*"; }
warn() { printf '\033[1;33m[WARN]\033[0m %s\n' "$*"; }
fail() { printf '\033[1;31m[FAIL]\033[0m %s\n' "$*"; }
ok()   { printf '\033[1;32m[OK]\033[0m   %s\n'   "$*"; }

# 2026-08-23 (audit P2-4): `column -t` 来自 bsdmainutils / util-linux，
# 不是 POSIX。在极简容器或 alpine 上可能缺失，回落到 `cat`。
if command -v column >/dev/null 2>&1; then
  COLUMN_CMD="column -t -s'|'"
else
  warn "column 未安装，输出将以原始 | 分隔展示"
  COLUMN_CMD="cat"
fi

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
  '"telemetry JSON field rescued by truncation"'
  '"telemetry JSONB field rescued by truncation"'
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

  # 3a) 模型维度行数对比。request_logs_hot.model 在历史部署中可能为空，
  #     因此使用实际写入的 outbound_model/client_model；WAL 使用 client_model。
  psql -h "$PGHOST" -p "$PGPORT" -U "$PGUSER" -d "$PGDATABASE" -At -F'|' <<SQL > "$OUT_DIR/sql-model-counts.txt"
SELECT
  COALESCE(NULLIF(outbound_model, ''), NULLIF(client_model, ''), '<unknown>') AS model,
  count(*) FILTER (WHERE request_status = 'success') AS success,
  count(*) FILTER (WHERE request_status = 'failure') AS failure,
  count(*) FILTER (WHERE request_status = 'client_disconnect') AS client_disc,
  count(*) FILTER (WHERE request_status NOT IN ('success','failure','client_disconnect') OR request_status IS NULL) AS other,
  count(*) AS total
FROM request_logs_hot
WHERE ts > now() - interval '$LOOKBACK_HOURS hour'
  AND lower(COALESCE(NULLIF(outbound_model, ''), NULLIF(client_model, ''))) IN ('minimax-m3','gpt-5.6-terra')
GROUP BY 1
ORDER BY 1;
SQL

  echo "  [3a] 各模型在 request_logs_hot 的状态分布（按实际模型字段）："
  $COLUMN_CMD "$OUT_DIR/sql-model-counts.txt" | sed 's/^/    /'
  echo

  # 3b) 以 WAL request_id 为基准对照 logs_hot。两表模型字段不一致时，
  #     仍能准确识别“WAL 有、最终 request_logs_hot 没有”的请求。
  psql -h "$PGHOST" -p "$PGPORT" -U "$PGUSER" -d "$PGDATABASE" -At -F'|' <<SQL > "$OUT_DIR/sql-wal-vs-logs.txt"
WITH wal AS (
  SELECT request_id, lower(client_model) AS model
  FROM request_wal_hot
  WHERE created_at > now() - interval '$LOOKBACK_HOURS hour'
    AND lower(client_model) IN ('minimax-m3','gpt-5.6-terra')
), matched AS (
  SELECT w.model, w.request_id, (r.request_id IS NOT NULL) AS log_present
  FROM wal w
  LEFT JOIN request_logs_hot r ON r.request_id = w.request_id
)
SELECT model,
       count(*) AS wal_rows,
       count(*) FILTER (WHERE log_present) AS log_rows,
       count(*) FILTER (WHERE NOT log_present) AS missing_rows
FROM matched
GROUP BY model
ORDER BY model;
SQL

  echo "  [3b] WAL request_id vs request_logs_hot（缺失 = WAL 有但最终行不存在）："
  $COLUMN_CMD "$OUT_DIR/sql-wal-vs-logs.txt" | sed 's/^/    /'
  echo

  # 3c) 正文写在 request_logs_bodies_hot，不在 request_logs_hot；主表的
  #     request_body/response_body 为 NULL 是预期架构。这里同时区分 side-table
  #     行缺失与 side-table 正文 NULL，避免把正常元数据行误报成 sanitize 丢失。
  psql -h "$PGHOST" -p "$PGPORT" -U "$PGUSER" -d "$PGDATABASE" -At -F'|' <<SQL > "$OUT_DIR/sql-null-rates.txt"
WITH logs AS (
  SELECT request_id, ts,
         lower(COALESCE(NULLIF(outbound_model, ''), NULLIF(client_model, ''))) AS model,
         outbound_body, routing_attempts, attachments, tool_calls, discard_events, t9_response_end_at
  FROM request_logs_hot
  WHERE ts > now() - interval '$LOOKBACK_HOURS hour'
    AND lower(COALESCE(NULLIF(outbound_model, ''), NULLIF(client_model, ''))) IN ('minimax-m3','gpt-5.6-terra')
)
SELECT l.model,
  round(100.0 * count(*) FILTER (WHERE b.request_id IS NULL) / count(*), 1) AS body_row_missing_pct,
  round(100.0 * count(*) FILTER (WHERE b.request_body IS NULL) / count(*), 1) AS req_body_null_pct,
  round(100.0 * count(*) FILTER (WHERE b.response_body IS NULL) / count(*), 1) AS resp_body_null_pct,
  round(100.0 * count(*) FILTER (WHERE l.outbound_body IS NULL) / count(*), 1) AS outbound_null_pct,
  round(100.0 * count(*) FILTER (WHERE l.routing_attempts IS NULL) / count(*), 1) AS routing_null_pct,
  round(100.0 * count(*) FILTER (WHERE l.attachments IS NULL) / count(*), 1) AS attachments_null_pct,
  round(100.0 * count(*) FILTER (WHERE l.t9_response_end_at IS NULL) / count(*), 1) AS t9_null_pct,
  round(100.0 * count(*) FILTER (WHERE l.tool_calls IS NULL) / count(*), 1) AS tool_calls_null_pct,
  round(100.0 * count(*) FILTER (WHERE l.discard_events IS NULL) / count(*), 1) AS discard_null_pct
FROM logs l
LEFT JOIN request_logs_bodies_hot b ON b.request_id = l.request_id
GROUP BY l.model
ORDER BY l.model;
SQL

  echo "  [3c] 正文 side-table 与关键字段 NULL 占比（主表正文 NULL 是预期）："
  $COLUMN_CMD "$OUT_DIR/sql-null-rates.txt" | sed 's/^/    /'

  # 3d) 失败 + 断开 的明细（带 error_kind）
  psql -h "$PGHOST" -p "$PGPORT" -U "$PGUSER" -d "$PGDATABASE" -At -F'|' <<SQL > "$OUT_DIR/sql-failure-detail.txt"
SELECT
  request_status, error_kind, failure_detail_code, failure_stage, count(*)
FROM request_logs_hot
WHERE ts > now() - interval '$LOOKBACK_HOURS hour'
  AND lower(COALESCE(NULLIF(outbound_model, ''), NULLIF(client_model, ''))) = 'minimax-m3'
  AND (request_status <> 'success' OR success = false)
GROUP BY request_status, error_kind, failure_detail_code, failure_stage
ORDER BY count(*) DESC
LIMIT 20;
SQL

  echo
  echo "  [3d] minimax-m3 非成功行的错误分布："
  $COLUMN_CMD "$OUT_DIR/sql-failure-detail.txt" | sed 's/^/    /'

  # 3e) side-table request_body 的长度分布。使用 request_body::text 后再取
  #     字节数；JSONB 没有 length()，且主表 request_body 按设计为空。
  psql -h "$PGHOST" -p "$PGPORT" -U "$PGUSER" -d "$PGDATABASE" -At -F'|' <<SQL > "$OUT_DIR/sql-rescued-candidates.txt"
SELECT
  l.model,
  count(*) FILTER (WHERE b.request_body IS NOT NULL AND octet_length(b.request_body::text) < 200) AS short_body,
  count(*) FILTER (WHERE b.request_body IS NOT NULL AND octet_length(b.request_body::text) BETWEEN 200 AND 2000) AS mid_body,
  count(*) FILTER (WHERE b.request_body IS NOT NULL AND octet_length(b.request_body::text) > 2000) AS long_body,
  count(*) FILTER (WHERE b.request_body IS NULL) AS body_null,
  count(*) AS total
FROM (
  SELECT request_id,
         lower(COALESCE(NULLIF(outbound_model, ''), NULLIF(client_model, ''))) AS model
  FROM request_logs_hot
  WHERE ts > now() - interval '$LOOKBACK_HOURS hour'
    AND lower(COALESCE(NULLIF(outbound_model, ''), NULLIF(client_model, ''))) IN ('minimax-m3','gpt-5.6-terra')
) l
LEFT JOIN request_logs_bodies_hot b ON b.request_id = l.request_id
GROUP BY l.model
ORDER BY l.model;
SQL

  echo
  echo "  [3e] side-table request_body 长度分布（短正文仅是 rescued 候选，需结合 metric）："
  $COLUMN_CMD "$OUT_DIR/sql-rescued-candidates.txt" | sed 's/^/    /'
fi

# ----------------------------------------------------------------------------
# 步骤 4：Prometheus 指标读数（新加的 counter）
# ----------------------------------------------------------------------------
echo
log "Step 4: 拉取 telemetry_sanitize_events_total 指标（如已升级新版本）"

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
echo "   → DB 写入路径有真丢失。检查 client.go 的 sanitizeJSONField /"
echo "     sanitizeRawJSONField 函数中 discarded 路径的 slog.Warn 与"
echo "     新指标 telemetry_sanitize_events_total{outcome=\"discarded\"}。"
echo
echo "2) 若 [3c] 显示 minimax-m3 的正文 side-table 行缺失或正文 NULL"
echo "   占比远高于 gpt-5.6-terra："
echo "   → 检查 sanitizeRequestLogEntry、upsertRequestLogBodies 及 DB 写入失败。"
echo "     注意 request_logs_hot.request_body/response_body 按设计为 NULL，"
echo "     不能单独作为 sanitize 丢失证据；以 request_logs_bodies_hot 为准。"
echo "     Step 6 起，部分场景（truncate 救回 JSON prefix）会改为 rescued，"
echo "     而不是 discarded；rescued 的累积计数见"
echo "     telemetry_sanitize_events_total{outcome=\"rescued\"}。"
echo
echo "3) 若 [3d] 全是 success 但 [3b] 行数对得上："
echo "   → 数据库没问题，前端查询或缓存问题；转 UI 排查。"
echo
echo "4) 若 [3a] minimax-m3 出现 client_disconnect / stream_interrupted："
echo "   → MiniMax API 不发 [DONE] 引起的虚惊失败（已被 c77a7d671 修复）。"
echo "     升级到包含 c77a7d671 的版本可消除此分支。"
echo
echo "5) 新指标告警建议（Prometheus alerting rules）："
echo "     - rate(telemetry_sanitize_events_total{outcome=\"discarded\"}"
echo "         ,field=~\"request_body|outbound_body|attachments\"}[5m]) > 0"
echo "     - rate(telemetry_sanitize_events_total{outcome=\"rescued\"}[5m])"
echo "         > 0.1  # 与正式告警规则保持一致"
echo
echo "原始输出文件位于：$OUT_DIR/"
echo "完成。"

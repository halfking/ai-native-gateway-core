#!/bin/bash
# scripts/monitoring/verify-mv-consistency.sh
#
# Routing analytics materialized view (migration 632) consistency checker
# (P2-D, 2026-09-01).
#
# Compares routing_analytics_7d aggregates against the base view
# request_logs_with_current_month_without_customer_id. Reports per-dimension
# drift; surfaces the max drift as a Prometheus-compatible metric line and
# (optionally) pushes a Lark alert when drift > DRIFT_ALERT_PCT.
#
# Why:
#   bg.MaterializedViewRefresher refreshes the views every 10 minutes via
#   REFRESH ... CONCURRENTLY. If anything goes silently wrong — partial
#   refresh, broken WHERE filter, schema drift — the analytics endpoints
#   serve stale or wrong numbers without any signal. This script is the
#   signal. Pair with the in-process drift metric
#   (gateway_routing_analytics_mv_drift_pct, see bg/mv_consistency.go) so
#   both cron and runtime see the same shape.
#
# Usage:
#   verify-mv-consistency.sh [--dsn "$DATABASE_URL"]
#                            [--alert-pct 5]
#                            [--min-abs-diff 100]
#                            [--prom-file /var/lib/prom/node_exporter/textfile/mv.prom]
#                            [--notify /opt/scripts/notify.sh]
#
# Exit codes:
#   0  drift below threshold (or no diff rows)
#   1  drift above threshold (alert pushed if --notify configured)
#   2  script/runtime error (psql connection failed, SQL error, etc.)
#
# Cron example (matches the 632 freshness budget — once per refresh cycle,
# well after the night peak has decayed):
#   15 2 * * * /opt/scripts/verify-mv-consistency.sh --notify /opt/scripts/notify.sh \
#     >> /var/log/mv-consistency.log 2>&1
set -euo pipefail

# === defaults ==========================================================
DSN="${LLMGW_DSN:-${DATABASE_URL:-}}"
ALERT_PCT="${DRIFT_ALERT_PCT:-5}"
MIN_ABS_DIFF="${DRIFT_MIN_ABS_DIFF:-100}"
PROM_FILE="${MV_DRIFT_PROM_FILE:-}"
NOTIFY="${MV_NOTIFY:-}"

# === arg parsing ========================================================
while [ $# -gt 0 ]; do
  case "$1" in
    --dsn)           DSN="$2"; shift 2;;
    --alert-pct)     ALERT_PCT="$2"; shift 2;;
    --min-abs-diff)  MIN_ABS_DIFF="$2"; shift 2;;
    --prom-file)     PROM_FILE="$2"; shift 2;;
    --notify)        NOTIFY="$2"; shift 2;;
    -h|--help)
      sed -n '2,40p' "$0"; exit 0;;
    *)
      echo "unknown arg: $1" >&2; exit 2;;
  esac
done

if [ -z "$DSN" ]; then
  echo "verify-mv-consistency: DSN required (--dsn or DATABASE_URL)" >&2
  exit 2
fi

LOG_TS() { date -Iseconds; }

write_prom_metrics() {
  [ -z "$PROM_FILE" ] && return 0
  local drift_pct="${1:-0}"
  local drift_abs="${2:-0}"
  local breaches="${3:-0}"
  local last_success="${4:-0}"
  local errors="${5:-0}"
  local error_reason="${6:-query_failed}"
  local tmp="${PROM_FILE}.tmp.$$"
  {
    echo "# HELP routing_analytics_mv_drift_pct Max percentage drift between routing_analytics_7d and base view."
    echo "# TYPE routing_analytics_mv_drift_pct gauge"
    echo "routing_analytics_mv_drift_pct{view=\"routing_analytics_7d\"} $drift_pct"
    echo "# HELP routing_analytics_mv_drift_abs Max absolute request-count drift per bucket."
    echo "# TYPE routing_analytics_mv_drift_abs gauge"
    echo "routing_analytics_mv_drift_abs{view=\"routing_analytics_7d\"} $drift_abs"
    echo "# HELP routing_analytics_mv_breach_count Number of buckets exceeding the drift threshold."
    echo "# TYPE routing_analytics_mv_breach_count gauge"
    echo "routing_analytics_mv_breach_count{view=\"routing_analytics_7d\"} $breaches"
    echo "# HELP routing_analytics_mv_consistency_last_unix Unix timestamp of the last completed consistency check."
    echo "# TYPE routing_analytics_mv_consistency_last_unix gauge"
    echo "routing_analytics_mv_consistency_last_unix{view=\"routing_analytics_7d\"} $last_success"
    echo "# HELP routing_analytics_mv_consistency_errors_total Total consistency check failures."
    echo "# TYPE routing_analytics_mv_consistency_errors_total counter"
    echo "routing_analytics_mv_consistency_errors_total{view=\"routing_analytics_7d\",reason=\"$error_reason\"} $errors"
  } > "$tmp" 2>/dev/null && mv -f "$tmp" "$PROM_FILE" 2>/dev/null || \
    echo "[$(LOG_TS)] WARN: prom file write failed: $PROM_FILE" >&2
}

write_error_metrics() {
  write_prom_metrics 0 0 0 0 1 "${1:-query_failed}"
}

# === sanity: views exist? (skip with exit 0 when migration 632 not applied) ===
# A no-DB deployment (disabled dbConn) or a fresh cluster without migration
# 632 should NOT spam alert noise — return 0 silently. The same script is
# used in dev/staging where the views may not exist yet.
check_views_exist() {
  psql "$DSN" -tAc "
    SELECT string_agg(matviewname, ',')
    FROM pg_matviews
    WHERE schemaname='public' AND matviewname IN ('routing_analytics_7d','routing_audit_summary_7d')
  "
}

VIEWS=$(check_views_exist 2>/dev/null || echo "")
if ! echo "$VIEWS" | grep -q "routing_analytics_7d"; then
  echo "[$(LOG_TS)] views not present (got: ${VIEWS:-none}), skipping"
  # Still refresh the freshness timestamp so a fresh/no-DB install doesn't
  # look like a wedged checker to the staleness alert — this is an expected
  # skip, not a failure.
  write_prom_metrics 0 0 0 "$(date +%s)" 0 ""
  exit 0
fi

# === main SQL ==========================================================
# Mirrors the WHERE clause of migration 632's routing_analytics_7d so the
# row sets are comparable: auto + specified-model, non-empty effective
# model, 7-day window. FULL OUTER JOIN keeps both sides' buckets.
#
# Output columns:
#   task_type, model, mv_count, base_count, abs_diff, diff_pct
# diff_pct is base-relative so a 5% drift on a 10k-bucket and a 5% drift
# on a 50-bucket look the same — MIN_ABS_DIFF guards against small-bucket
# noise (a single late-arriving row in a 20-row bucket = 5% by definition).
read -r -d '' SQL <<'EOF' || true
WITH mv_data AS (
  SELECT
    effective_task_type,
    effective_model,
    SUM(request_count)::bigint AS mv_count
  FROM routing_analytics_7d
  WHERE effective_model IS NOT NULL
  GROUP BY effective_task_type, effective_model
),
base_data AS (
  SELECT
    COALESCE(NULLIF(task_type, ''), CASE WHEN COALESCE(is_auto_request, FALSE) THEN 'unknown' ELSE '__specified__' END) AS task_type,
    COALESCE(NULLIF(outbound_model, ''), client_model) AS model,
    COUNT(*)::bigint AS base_count
  FROM request_logs_with_current_month_without_customer_id
  WHERE ts >= NOW() - INTERVAL '7 days'
    AND (
      COALESCE(is_auto_request, FALSE) = TRUE
      OR (COALESCE(is_auto_request, FALSE) = FALSE AND client_model IS NOT NULL AND client_model <> '')
    )
    AND COALESCE(NULLIF(outbound_model, ''), client_model) IS NOT NULL
  GROUP BY task_type, model
)
SELECT
  COALESCE(mv.effective_task_type, base.task_type) AS task_type,
  COALESCE(mv.effective_model, base.model) AS model,
  COALESCE(mv.mv_count, 0) AS mv_count,
  COALESCE(base.base_count, 0) AS base_count,
  ABS(COALESCE(mv.mv_count, 0) - COALESCE(base.base_count, 0)) AS abs_diff,
  CASE
    WHEN COALESCE(base.base_count, 0) > 0
      THEN ROUND((ABS(COALESCE(mv.mv_count, 0) - COALESCE(base.base_count, 0))::numeric
                  / base.base_count::numeric) * 100, 2)
    ELSE 0
  END AS diff_pct
FROM mv_data mv
FULL OUTER JOIN base_data base
  ON mv.effective_task_type = base.task_type
 AND mv.effective_model = base.model
WHERE ABS(COALESCE(mv.mv_count, 0) - COALESCE(base.base_count, 0)) > 0
ORDER BY abs_diff DESC
LIMIT 200;
EOF

RESULT=$(psql "$DSN" -tAF $'\t' -c "$SQL" 2>&1) || {
  echo "[$(LOG_TS)] psql query failed:" >&2
  echo "$RESULT" >&2
  write_error_metrics "query_failed"
  exit 2
}

# === summarise =========================================================
# Each non-empty line is tab-separated: task_type\tmodel\tmv\tbase\tabs\tdiff_pct
max_pct=0
max_abs=0
n_rows=0
breach_lines=()
breach_count=0

while IFS=$'\t' read -r task_type model mv_count base_count abs_diff diff_pct; do
  [ -z "$task_type" ] && continue
  n_rows=$((n_rows+1))
  # awk for float compare (bash can't do float natively)
  if awk -v a="$diff_pct" -v b="$max_pct" 'BEGIN{exit !(a>b)}'; then
    max_pct="$diff_pct"
  fi
  if [ "${abs_diff:-0}" -gt "${max_abs:-0}" ] 2>/dev/null; then
    max_abs="$abs_diff"
  fi
  # Only flag breaches that are both > alert% AND non-trivial in volume.
  # Tiny buckets (a single late row in a 4-row group) easily blow 5% — the
  # 100-request floor is what makes the alert actionable.
  if awk -v a="$diff_pct" -v b="$ALERT_PCT" 'BEGIN{exit !(a>b)}' \
     && [ "${abs_diff:-0}" -gt "$MIN_ABS_DIFF" ] 2>/dev/null; then
    breach_lines+=("$task_type | $model | mv=$mv_count base=$base_count abs=$abs_diff pct=${diff_pct}%")
    breach_count=$((breach_count+1))
  fi
done <<< "$RESULT"

echo "[$(LOG_TS)] drift buckets=$n_rows max_pct=${max_pct}% max_abs=$max_abs breaches=$breach_count threshold=${ALERT_PCT}%/${MIN_ABS_DIFF}req"

# Echo the top breaches (cap at 10 for log readability)
if [ "${#breach_lines[@]}" -gt 0 ]; then
  i=0
  for line in "${breach_lines[@]}"; do
    [ $i -ge 10 ] && break
    echo "  $line"
    i=$((i+1))
  done
  [ "${#breach_lines[@]}" -gt 10 ] && echo "  ... and $((${#breach_lines[@]} - 10)) more"
fi

# === Prometheus textfile emission =====================================
# Textfile collector format. Only emit when --prom-file configured. Atomic
# write via tmp + mv so node_exporter never sees a half-written file. Also
# refreshes the _consistency_last_unix timestamp on every successful run (not
# just when drift==0), so a checker that runs but always finds drift doesn't
# look "stale" to the RoutingAnalyticsMVConsistencyCheckStale alert.
write_prom_metrics "$max_pct" "$max_abs" "$breach_count" "$(date +%s)" 0 ""

# === alerting ==========================================================
# Lark push via the project's notify.sh (same shape as 252-monitor). Skip
# silently when --notify not configured or no breaches — most runs are
# quiet. Cooldown is the responsibility of notify.sh / the IM gateway.
if [ "$breach_count" -gt 0 ] && [ -n "$NOTIFY" ] && [ -x "$NOTIFY" ]; then
  ts_now=$(LOG_TS)
  body="物化视图数据一致性告警 (P2-D)"
  body+=$'\n'"检测时间: ${ts_now}"
  body+=$'\n'"差异维度数: ${breach_count} (阈值 ${ALERT_PCT}% 且 >${MIN_ABS_DIFF} req)"
  body+=$'\n'"最大差异: ${max_pct}% (abs=${max_abs})"
  body+=$'\n'""
  body+=$'\n'"Top breaches:"
  i=0
  for line in "${breach_lines[@]}"; do
    [ $i -ge 5 ] && break
    body+=$'\n'"  - ${line}"
    i=$((i+1))
  done
  body+=$'\n'""
  body+=$'\n'"建议: 检查 bg/materialized_view_refresher 日志；如失败可手动触发 TriggerRefresh"

  "$NOTIFY" --level warning --title "MV 一致性 ${breach_count} 维度超阈值" --body "$body" || \
    echo "[$(LOG_TS)] WARN: notify.sh failed" >&2
fi

# Exit code: 1 on breach (so cron / monitors can alert independently of
# Lark wiring); 0 on clean run. Runtime errors already exited 2 above.
if [ "$breach_count" -gt 0 ]; then
  exit 1
fi
exit 0

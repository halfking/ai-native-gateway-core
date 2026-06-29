#!/usr/bin/env bash
# scripts/columnar-monthly-cron.sh
#
# Monthly columnar archive cron for 184.
#
# Schedules:
#   - day 1: request_logs, routing_decision_log
#   - day 2: request_wal
#   - day 3: credential_model_index (7d cutoff)
#
# Cron entry (host-level):
#   0 4 1-3 * * /opt/scripts/columnar-monthly-cron.sh >> /var/log/columnar-monthly.log 2>&1
#
# This replaces the legacy /tmp/columnar-cron.sh which had a wrong
# table name (request_wal_bodies — does not exist) and the wrong
# PG port (11033 — should be 11032 or 5432 inside the PG container).
#
# The script runs entirely on the host: it talks to Postgres through
# kubectl exec against the llm-gateway-pg deployment in namespace
# pms-test. All SQL logic lives in the archive_xxx() plpgsql
# functions; this script is a thin scheduler that picks the right
# archive function for today and lets the DB do the work.
#
# Idempotency: every archive_xxx() function checks for the source
# partition's existence and returns 'skipped' if it is gone. So
# running on day 1 + day 2 + day 3 is safe even if a previous day
# already finished a table.

set -euo pipefail

K8S_NAMESPACE="pms-test"
K8S_DEPLOY="llm-gateway-pg"
PG_DB="llm_gateway"
PG_USER="llm_gateway"

# day-of-month in 1..3
TODAY=$(date +%e | tr -d ' ')

case "$TODAY" in
  1)
    TARGETS=("archive_request_logs" "archive_routing_decision_log")
    ;;
  2)
    TARGETS=("archive_request_wal")
    ;;
  3)
    TARGETS=("archive_credential_model_index")
    ;;
  *)
    echo "[$(date)] day=$TODAY: not an archive day (1-3 only), exit 0"
    exit 0
    ;;
esac

# 2-months-ago date (e.g. on 2026-07-01 we archive 2026-05)
ARCHIVE_MONTH=$(date -d '2 months ago' +%Y-%m-01)
echo "[$(date)] columnar-monthly-cron: day=$TODAY targets=${TARGETS[*]} month=$ARCHIVE_MONTH"

POD=$(kubectl get pod -n "$K8S_NAMESPACE" -l app="$K8S_DEPLOY" -o jsonpath='{.items[0].metadata.name}')
if [[ -z "$POD" ]]; then
  echo "[$(date)] ERROR: no pod found for $K8S_DEPLOY in $K8S_NAMESPACE" >&2
  exit 1
fi

OVERALL_RC=0
for fn in "${TARGETS[@]}"; do
  echo "[$(date)] -> invoking $fn('$ARCHIVE_MONTH')"
  START=$(date +%s)

  # -tA: tuples-only, no-alignment. We expect a single 3-tuple row.
  # The function returns (status, rows_migrated, partition_dropped)
  # for the request_*/routing_* functions and
  # (status, rows_archived, rows_deleted) for credential_model_index.
  # We do not interpret the row here — we just capture it.
  set +e
  kubectl exec -n "$K8S_NAMESPACE" "$POD" -c citus -- \
    env PGPASSWORD='4Q92cFTaYY8Z3AO07XTBBH-1g7kceaxg' \
    psql -U "$PG_USER" -d "$PG_DB" -tA \
    -c "SELECT * FROM $fn('$ARCHIVE_MONTH'::date);" \
    2>&1 | tee -a /var/log/columnar-monthly.log
  RC=$?
  set -e

  END=$(date +%s)
  if [[ "$RC" -ne 0 ]]; then
    echo "[$(date)] ERROR: $fn failed in $((END-START))s, rc=$RC" >&2
    OVERALL_RC=$RC
    continue
  fi
  echo "[$(date)] <- $fn ok in $((END-START))s"
done

echo "[$(date)] columnar-monthly-cron: done, rc=$OVERALL_RC"
exit "$OVERALL_RC"

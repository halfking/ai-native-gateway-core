#!/usr/bin/env bash
# scripts/columnar-daily-cron.sh
#
# Long-term columnar invariant guard for the llm-gateway-go PG primary
# in 184 (and 71). Runs nightly.
#
# Responsibilities:
#   1. Drain the body-table backfill: call backfill_request_logs_bodies()
#      until rows_pending_backfill = 0. Memory-bounded (one batch per
#      implicit transaction).
#   2. Run columnar_heal() to convert any new heap partitions of
#      INSERT-only parents. Defensive safety net — the event trigger
#      from phase 23 / 02 should already have caught these at DDL
#      time, but cron heals anything that slipped through.
#   3. Emit a structured diff report: columnar_drift_report() + the
#      backfill progress view. Logged to /var/log/columnar-daily.log
#      and (optionally) shipped via the gateway's own logger.
#
# Cron entry (host-level):
#   15 3 * * *  /opt/scripts/columnar-daily-cron.sh >> /var/log/columnar-daily.log 2>&1
#
# Idempotent. Safe to run alongside the existing columnar-monthly-cron.sh.

set -euo pipefail

K8S_NAMESPACE="pms-test"
K8S_DEPLOY="llm-gateway-pg"
PG_DB="llm_gateway"
PG_USER="llm_gateway"
LOG_FILE="${LOG_FILE:-/var/log/columnar-daily.log}"

# Decrypt password from .env.<target>.enc if available
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
if [[ -f "$PROJECT_ROOT/.env.184.enc" ]]; then
    export SOPS_AGE_KEY_FILE="${SOPS_AGE_KEY_FILE:-$HOME/.config/sops/age/keys.txt}"
    DB_PASSWORD=$(sops -d "$PROJECT_ROOT/.env.184.enc" 2>/dev/null | grep '^export DB_PASSWORD=' | cut -d= -f2 | tr -d '"')
fi
: "${DB_PASSWORD:?must set DB_PASSWORD}"

log() {
    local ts
    ts=$(date '+%Y-%m-%dT%H:%M:%S%z')
    echo "[$ts] $*" | tee -a "$LOG_FILE"
}

err() {
    local ts
    ts=$(date '+%Y-%m-%dT%H:%M:%S%z')
    echo "[$ts] ERROR: $*" | tee -a "$LOG_FILE" >&2
}

POD=$(kubectl get pod -n "$K8S_NAMESPACE" -l app="$K8S_DEPLOY" -o jsonpath='{.items[0].metadata.name}')
if [[ -z "$POD" ]]; then
    err "no pod found for $K8S_DEPLOY in $K8S_NAMESPACE"
    exit 1
fi

run_psql() {
    kubectl exec -n "$K8S_NAMESPACE" "$POD" -c citus -- \
        env PGPASSWORD="$DB_PASSWORD" \
        psql -U "$PG_USER" -d "$PG_DB" -X -tA -v ON_ERROR_STOP=1 "$@"
}

# ============================================================
# Stage 1: columnar_drift_report()
# ============================================================
log "stage 1: columnar_drift_report"
DRIFT=$(run_psql -c "SELECT parent_name || ' compliant=' || compliant_count || ' noncompliant=' || noncompliant_count || ' total=' || pg_size_pretty(total_size_bytes) FROM columnar_drift_report();")
echo "$DRIFT" | while IFS= read -r line; do
    log "  drift: $line"
done

# ============================================================
# Stage 2: columnar_heal() — auto-repair INSERT-only drift
# ============================================================
log "stage 2: columnar_heal"
HEAL=$(run_psql -c "SELECT parent_name || '.' || partition_name || ' (' || pre_size_bytes || ' B -> ' || post_size_bytes || ' B)' FROM columnar_heal() WHERE converted;")
if [[ -z "$HEAL" ]]; then
    log "  heal: nothing to convert"
else
    echo "$HEAL" | while IFS= read -r line; do
        log "  heal: converted $line"
    done
fi

# ============================================================
# Stage 3: drain the body-table backfill
# ============================================================
log "stage 3: drain body-table backfill"
BATCH="${BATCH:-200}"
MAX_BATCHES="${MAX_BATCHES:-1000}"
batches=0
while :; do
    PENDING=$(run_psql -c "SELECT rows_pending_backfill FROM request_logs_bodies_progress")
    PENDING=$(echo "$PENDING" | tr -d '[:space:]')
    if [[ -z "$PENDING" || "$PENDING" -le 0 ]]; then
        log "  backfill: rows_pending=0, done"
        break
    fi

    BATCH_LOG=$(run_psql -c "CALL backfill_request_logs_bodies($BATCH);" 2>&1) || true
    batches=$((batches + 1))

    if (( batches >= MAX_BATCHES )); then
        err "  backfill: reached MAX_BATCHES=$MAX_BATCHES, stopping (pending=$PENDING). Will continue tomorrow."
        break
    fi

    log "  backfill batch $batches done (was $PENDING pending)"
done

# ============================================================
# Stage 4: final summary
# ============================================================
log "stage 4: final summary"
run_psql -c "SELECT 'source=' || source_rows_with_body || ' bodies=' || bodies_rows || ' pending=' || rows_pending_backfill FROM request_logs_bodies_progress" | while IFS= read -r line; do
    log "  $line"
done
run_psql -c "SELECT 'after_heal_compliance: compliant=' || sum(compliant_count)::text || ' noncompliant=' || sum(noncompliant_count)::text FROM columnar_drift_report();" | while IFS= read -r line; do
    log "  $line"
done

log "done."

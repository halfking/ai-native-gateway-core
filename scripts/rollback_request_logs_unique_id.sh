#!/usr/bin/env bash
# scripts/rollback_request_logs_unique_id.sh
# 2026-06-26 — Roll back migration 301 (request_logs unique on request_id).
#
# Use case:
#   Migration 301 was applied but a regression or operator decision requires
#   reverting to the previous (request_id, ts) constraint shape.
#
# WARNING:
#   This rollback DELETES duplicate rows created AFTER migration 301 ran
#   (keeping the earliest row per request_id). If your application is still
#   emitting duplicate INSERTs (e.g. you also need to revert telemetry/client.go),
#   the database will accumulate new duplicates again.
#
# Usage:
#   LLM_GATEWAY_DATABASE_URL=postgres://... ./scripts/rollback_request_logs_unique_id.sh
#
# Optional env:
#   AUTO_CONFIRM=1   Skip the interactive confirmation prompt
#   DRY_RUN=1        Print the rollback SQL without executing
#
# Exit codes: same as deploy_request_logs_unique_id.sh (1..4)

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"

DOWN_FILE="${REPO_ROOT}/db/migrations/301_request_logs_unique_request_id_only.down.sql"

LOG_PREFIX="[$(date '+%Y-%m-%d %H:%M:%S')]"

if [[ -n "${LLM_GATEWAY_DATABASE_URL:-}" ]]; then
    DSN="${LLM_GATEWAY_DATABASE_URL}"
elif [[ -n "${PG_HOST:-}" ]]; then
    DSN="postgres://${PG_USER:-postgres}:${PG_PASSWORD:-}@${PG_HOST}:${PG_PORT:-5432}/${PG_DATABASE:-postgres}?sslmode=disable"
else
    echo "${LOG_PREFIX} ERROR: Set LLM_GATEWAY_DATABASE_URL" >&2
    exit 2
fi

DSN_MASKED="$(echo "${DSN}" | sed -E 's#://[^@/]+:([^@]+)@#://***:***@#')"
echo "${LOG_PREFIX} Target DSN: ${DSN_MASKED}"

if ! command -v psql >/dev/null 2>&1; then
    echo "${LOG_PREFIX} ERROR: psql not found in PATH" >&2
    exit 2
fi

PSQL_CMD=(psql "${DSN}" --set ON_ERROR_STOP=on -v ON_ERROR_STOP=1 -X --quiet)

if [[ ! -f "${DOWN_FILE}" ]]; then
    echo "${LOG_PREFIX} ERROR: rollback file missing: ${DOWN_FILE}" >&2
    exit 1
fi

echo ""
echo "${LOG_PREFIX} ════════════════════════════════════════════════════════════════════"
echo "${LOG_PREFIX} Rolling back migration 301 — restoring (request_id, ts) constraint"
echo "${LOG_PREFIX} ════════════════════════════════════════════════════════════════════"

if [[ "${DRY_RUN:-0}" == "1" ]]; then
    echo "${LOG_PREFIX} DRY_RUN=1 — rollback content (NOT executed):"
    echo "-------------------------------------------------------------------"
    cat "${DOWN_FILE}"
    echo "-------------------------------------------------------------------"
    echo "${LOG_PREFIX} Dry-run complete. No DB writes performed."
    exit 0
fi

if [[ "${AUTO_CONFIRM:-0}" != "1" ]]; then
    echo ""
    echo "${LOG_PREFIX} About to:"
    echo "  - DROP INDEX IF EXISTS idx_request_logs_request_id_unique"
    echo "  - CREATE UNIQUE INDEX idx_request_logs_request_id_ts_unique (on (request_id, ts))"
    echo ""
    echo "${LOG_PREFIX} ⚠️  WARNING: This rolls back the duplicate-row protection."
    echo "${LOG_PREFIX}    New duplicate rows can be created by INSERT ts=now() races."
    echo ""
    echo "${LOG_PREFIX} Press ENTER to continue, or Ctrl-C to abort (10s timeout)..."
    read -t 10 -r || {
        rc=$?
        if [[ $rc -gt 128 ]]; then
            echo "${LOG_PREFIX} Auto-confirm timeout — aborting."
            exit 1
        fi
    }
fi

if ! "${PSQL_CMD[@]}" -f "${DOWN_FILE}"; then
    rc=$?
    echo "${LOG_PREFIX} ERROR: rollback failed (rc=${rc})" >&2
    exit 3
fi

echo ""
echo "${LOG_PREFIX} ════════════════════════════════════════════════════════════════════"
echo "${LOG_PREFIX} ✅ ROLLBACK COMPLETE"
echo "${LOG_PREFIX} ════════════════════════════════════════════════════════════════════"
echo ""
echo "${LOG_PREFIX} After rollback, the application code may emit duplicate INSERTs"
echo "${LOG_PREFIX} again. To fully revert, you must also:"
echo "  1. Revert telemetry/client.go (insertRequestLog + upsertRequestLogFallback)"
echo "  2. Revert db/db.go (remove ensureRequestLogsUniqueIndex call)"
echo "  3. Rebuild + redeploy the gateway"
echo ""

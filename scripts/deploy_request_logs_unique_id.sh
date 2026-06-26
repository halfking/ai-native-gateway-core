#!/usr/bin/env bash
# scripts/deploy_request_logs_unique_id.sh
# 2026-06-26 — deploy migration 301 to fix the request_logs duplicate-row bug.
#
# Bug summary:
#   A glm-5.1 request that retried 智谱 3 times before succeeding on nvidia nim
#   produced 4 rows in request_logs, all subsequently updated to "nvidia nim
#   success" — because the (request_id, ts) unique constraint allowed ts=now()
#   to differ per INSERT, and UPDATE ... WHERE request_id=$1 matched all rows.
#
# Fix:
#   Replace UNIQUE (request_id, ts) with UNIQUE (request_id) only. After this
#   migration, every logical user request corresponds to exactly one row in
#   request_logs, regardless of retry / fallback / async-retry activity.
#
# Usage:
#   # Production deploy with confirmation prompt
#   ./scripts/deploy_request_logs_unique_id.sh
#
#   # Non-interactive (CI / automation)
#   AUTO_CONFIRM=1 ./scripts/deploy_request_logs_unique_id.sh
#
#   # Dry run (no DB writes)
#   DRY_RUN=1 ./scripts/deploy_request_logs_unique_id.sh
#
# Required env:
#   LLM_GATEWAY_DATABASE_URL  — Postgres URL, e.g. postgres://user:pass@host:5432/db?sslmode=disable
#
# Optional env:
#   PG_HOST, PG_PORT, PG_USER, PG_PASSWORD, PG_DATABASE — alternative connection params
#
# Exit codes:
#   0  success
#   1  precheck failed (refusing to proceed)
#   2  database connection failed
#   3  migration failed
#   4  post-verification failed (something is wrong; investigate)

set -euo pipefail

***REMOVED***
# Paths and helpers
***REMOVED***

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"

MIGRATION_FILE="${REPO_ROOT}/db/migrations/301_request_logs_unique_request_id_only.sql"
PRECHECK_FILE="${REPO_ROOT}/db/scripts/pre_migration_check.sql"
VERIFY_FILE="${REPO_ROOT}/db/scripts/verify_request_logs_unique.sql"

LOG_PREFIX="[$(date '+%Y-%m-%d %H:%M:%S')]"

***REMOVED***
# Resolve DSN
***REMOVED***

if [[ -n "${LLM_GATEWAY_DATABASE_URL:-}" ]]; then
    DSN="${LLM_GATEWAY_DATABASE_URL}"
elif [[ -n "${PG_HOST:-}" ]]; then
    DSN="postgres://${PG_USER:-postgres}:${PG_PASSWORD:-}@${PG_HOST}:${PG_PORT:-5432}/${PG_DATABASE:-postgres}?sslmode=disable"
else
    echo "${LOG_PREFIX} ERROR: Set LLM_GATEWAY_DATABASE_URL or PG_HOST/PG_USER/PG_PASSWORD/PG_DATABASE" >&2
    echo "${LOG_PREFIX} Example:" >&2
    echo "  LLM_GATEWAY_DATABASE_URL=postgres://user:pass@host:5432/llm_gateway?sslmode=disable \\" >&2
    echo "    $0" >&2
    exit 2
fi

# Mask password when echoing DSN.
DSN_MASKED="$(echo "${DSN}" | sed -E 's#://[^@/]+:([^@]+)@#://***:***@#')"
echo "${LOG_PREFIX} Target DSN: ${DSN_MASKED}"

***REMOVED***
# Find psql
***REMOVED***

if ! command -v psql >/dev/null 2>&1; then
    echo "${LOG_PREFIX} ERROR: psql not found in PATH" >&2
    echo "${LOG_PREFIX} Install PostgreSQL client tools (apt-get install postgresql-client / brew install postgresql)" >&2
    exit 2
fi

PSQL_CMD=(psql "${DSN}" --set ON_ERROR_STOP=on -v ON_ERROR_STOP=1 -X --quiet)

***REMOVED***
# Pre-flight: required files exist
***REMOVED***

for f in "${MIGRATION_FILE}" "${PRECHECK_FILE}" "${VERIFY_FILE}"; do
    if [[ ! -f "${f}" ]]; then
        echo "${LOG_PREFIX} ERROR: required file missing: ${f}" >&2
        exit 1
    fi
done

***REMOVED***
# Step 1: Pre-migration check
***REMOVED***

echo ""
echo "${LOG_PREFIX} ════════════════════════════════════════════════════════════════════"
echo "${LOG_PREFIX} Step 1/4: Pre-migration check"
echo "${LOG_PREFIX} ════════════════════════════════════════════════════════════════════"
echo ""

"${PSQL_CMD[@]}" -f "${PRECHECK_FILE}" || {
    rc=$?
    echo "${LOG_PREFIX} ERROR: precheck query failed (rc=${rc})" >&2
    exit 2
}

***REMOVED***
# Step 2: Confirmation prompt
***REMOVED***

echo ""
echo "${LOG_PREFIX} ════════════════════════════════════════════════════════════════════"
echo "${LOG_PREFIX} Step 2/4: Apply migration 301 (request_logs unique on request_id)"
echo "${LOG_PREFIX} ════════════════════════════════════════════════════════════════════"

if [[ "${DRY_RUN:-0}" == "1" ]]; then
    echo "${LOG_PREFIX} DRY_RUN=1 — migration content (NOT executed):"
    echo "-------------------------------------------------------------------"
    cat "${MIGRATION_FILE}"
    echo "-------------------------------------------------------------------"
    echo "${LOG_PREFIX} Dry-run complete. No DB writes performed."
    exit 0
fi

if [[ "${AUTO_CONFIRM:-0}" != "1" ]]; then
    echo ""
    echo "${LOG_PREFIX} About to apply:"
    echo "  - DELETE duplicates from request_logs (last 7 days, keep earliest)"
    echo "  - DROP INDEX IF EXISTS idx_request_logs_request_id_ts_unique"
    echo "  - CREATE UNIQUE INDEX idx_request_logs_request_id_unique"
    echo ""
    echo "${LOG_PREFIX} Press ENTER to continue, or Ctrl-C to abort (you have 10s)..."
    read -t 10 -r || {
        rc=$?
        if [[ $rc -gt 128 ]]; then
            echo "${LOG_PREFIX} Auto-confirm timeout — aborting."
            exit 1
        fi
    }
fi

***REMOVED***
# Step 3: Apply migration
***REMOVED***

echo ""
echo "${LOG_PREFIX} ════════════════════════════════════════════════════════════════════"
echo "${LOG_PREFIX} Step 3/4: Applying migration"
echo "${LOG_PREFIX} ════════════════════════════════════════════════════════════════════"

if ! "${PSQL_CMD[@]}" -f "${MIGRATION_FILE}"; then
    rc=$?
    echo "${LOG_PREFIX} ERROR: migration failed (rc=${rc})" >&2
    echo "${LOG_PREFIX} The database may be in a partial state. Inspect with:" >&2
    echo "  psql \"${DSN_MASKED}\" -c \"\\d+ request_logs\"" >&2
    exit 3
fi

echo "${LOG_PREFIX} Migration applied successfully."

***REMOVED***
# Step 4: Post-verification
***REMOVED***

echo ""
echo "${LOG_PREFIX} ════════════════════════════════════════════════════════════════════"
echo "${LOG_PREFIX} Step 4/4: Post-verification"
echo "${LOG_PREFIX} ════════════════════════════════════════════════════════════════════"

if ! "${PSQL_CMD[@]}" -f "${VERIFY_FILE}"; then
    rc=$?
    echo "${LOG_PREFIX} ERROR: post-verification failed (rc=${rc})" >&2
    echo "${LOG_PREFIX} Investigate with: psql \"${DSN_MASKED}\" -c \"SELECT * FROM pg_indexes WHERE tablename='request_logs' AND indexname LIKE '%request_id%';\"" >&2
    exit 4
fi

echo ""
echo "${LOG_PREFIX} ════════════════════════════════════════════════════════════════════"
echo "${LOG_PREFIX} ✅ DEPLOYMENT COMPLETE"
echo "${LOG_PREFIX} ════════════════════════════════════════════════════════════════════"
echo ""
echo "${LOG_PREFIX} Next steps:"
echo "  1. Restart the gateway so db.Open() applies ensureRequestLogsUniqueIndex"
echo "     on any fresh instances that boot from scratch."
echo "  2. (Optional) Run live tests:"
echo "       LLM_GATEWAY_PG_TEST_URL=\"${DSN_MASKED}\" \\"
echo "         go test ./telemetry/ -run TestRequestLogUnique -v"
echo "  3. Monitor the gateway logs for the line:"
echo "       'request_logs unique constraint enforced on (request_id) only'"
echo ""

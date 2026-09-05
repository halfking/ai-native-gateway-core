#!/usr/bin/env bash
# -----------------------------------------------------------------------------
# File:          scripts/audit/psql-isolated.sh
# Purpose:       Run psql against the isolated audit PG. Sources /tmp/audit-pg.env
#                if present; otherwise assumes the container is already up.
#
# Usage:
#   bash scripts/audit/psql-isolated.sh -c "SELECT 1;"
#   bash scripts/audit/psql-isolated.sh -f scripts/audit/sql/min-prereqs.sql
#   cat foo.sql | bash scripts/audit/psql-isolated.sh
# -----------------------------------------------------------------------------
set -euo pipefail
if [[ -f /tmp/audit-pg.env ]]; then
    set -a
    # shellcheck disable=SC1091
    source /tmp/audit-pg.env
    set +a
fi
: "${AUDIT_PG_USER:?AUDIT_PG_USER not set; run start-isolated-pg.sh first}"
: "${AUDIT_PG_DB:?AUDIT_PG_DB not set; run start-isolated-pg.sh first}"

# If -f is the first non-flag arg, read the file and pipe via stdin so the
# docker exec wrapper can forward it to psql. Otherwise pass the args through,
# including any piped stdin. psql stops on the first error by default; callers
# that intentionally need to inspect expected per-statement failures must opt
# in explicitly with -v ON_ERROR_STOP=0.
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
if [[ "${1:-}" == "-f" && -n "${2:-}" ]]; then
    shift
    exec docker exec -i -e PGPASSWORD="$AUDIT_PG_PASS" kx-citus \
        psql -U "$AUDIT_PG_USER" -d "$AUDIT_PG_DB" \
        "$@" < "$1"
fi

exec docker exec -i -e PGPASSWORD="$AUDIT_PG_PASS" kx-citus \
    psql -U "$AUDIT_PG_USER" -d "$AUDIT_PG_DB" "$@"

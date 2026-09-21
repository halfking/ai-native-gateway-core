#!/usr/bin/env bash
# -----------------------------------------------------------------------------
# File:          scripts/audit/bootstrap-isolated-pg.sh
# Purpose:       Bootstrap a fresh isolated PG with the same schema the
#                installer produces, then apply all startup migrations
#                (511..631) in order. After this script completes, the audit
#                container is functionally equivalent to a fresh installer
#                deployment minus the embed-only data.
#
# Usage:
#   bash scripts/audit/bootstrap-isolated-pg.sh
#   bash scripts/audit/bootstrap-isolated-pg.sh --skip-startup   # only prereqs + 01 + 02
# -----------------------------------------------------------------------------
set -euo pipefail

if [[ ! -f /tmp/audit-pg.env ]]; then
    echo "ERROR: /tmp/audit-pg.env missing; run start-isolated-pg.sh first." >&2
    exit 1
fi
set -a
# shellcheck disable=SC1091
source /tmp/audit-pg.env
set +a

SKIP_STARTUP=0
for arg in "$@"; do
    case "$arg" in
        --skip-startup) SKIP_STARTUP=1 ;;
        *) echo "unknown arg: $arg"; exit 1 ;;
    esac
done

REPO_ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$REPO_ROOT"

run_sql() {
    local label="$1"
    local path="$2"
    echo "▶ $label  ($path)"
    docker exec -i -e PGPASSWORD="$AUDIT_PG_PASS" llm-gateway-audit-pg \
        psql -U "$AUDIT_PG_USER" -d "$AUDIT_PG_DB" -v ON_ERROR_STOP=1 --single-transaction \
        < "$path" >/tmp/audit-pg.last.log 2>&1 || {
        echo "ERROR: $label failed. Tail of /tmp/audit-pg.last.log:"
        tail -40 /tmp/audit-pg.last.log
        exit 1
    }
    echo "  ✓ $label"
}

# 1. prereqs
run_sql "00-prereqs" sql/schema/00-prereqs.sql

# 2. 01-schema.sql is a large pg_dump; single-transaction would lock the DB for
# minutes. Split into per-statement execution to keep visibility on partial
# progress. Re-use the file as-is.
echo "▶ 01-schema (large; statement-by-statement) ..."
PGOPTIONS='-c statement_timeout=0' docker exec -i -e PGPASSWORD="$AUDIT_PG_PASS" \
    -e PGOPTIONS='-c statement_timeout=0' \
    llm-gateway-audit-pg \
    psql -U "$AUDIT_PG_USER" -d "$AUDIT_PG_DB" -v ON_ERROR_STOP=1 \
    < sql/schema/01-schema.sql >/tmp/audit-pg.last.log 2>&1 || {
    echo "ERROR: 01-schema failed. Tail of /tmp/audit-pg.last.log:"
    tail -40 /tmp/audit-pg.last.log
    exit 1
}
echo "  ✓ 01-schema"

# 3. seed
run_sql "02-seed" sql/schema/02-seed.sql

# 4. startup migrations (511..631) in numeric order
if [[ "$SKIP_STARTUP" != "1" ]]; then
    cd "$REPO_ROOT"
    shopt -s nullglob
    files=(sql/migrations/startup/[0-9]*.sql)
    # Filter out repair_* and *.down.sql
    files=("${files[@]##*/}")
    files=($(printf '%s\n' "${files[@]}" | grep -E '^[0-9]+_' | sort))
    echo "▶ applying ${#files[@]} startup migrations ..."
    for f in "${files[@]}"; do
        if [[ ! -f "sql/migrations/startup/$f" ]]; then
            echo "  ! missing: $f"
            continue
        fi
        if ! docker exec -i -e PGPASSWORD="$AUDIT_PG_PASS" \
            llm-gateway-audit-pg \
            psql -U "$AUDIT_PG_USER" -d "$AUDIT_PG_DB" -v ON_ERROR_STOP=1 \
            < "sql/migrations/startup/$f" >/tmp/audit-pg.last.log 2>&1; then
            echo "ERROR: $f failed. Tail of /tmp/audit-pg.last.log:"
            tail -40 /tmp/audit-pg.last.log
            exit 1
        fi
        printf "  ✓ %s\n" "$f"
    done
    cd "$REPO_ROOT"
fi

# 5. final probe
echo "▶ post-bootstrap probe ..."
docker exec -e PGPASSWORD="$AUDIT_PG_PASS" llm-gateway-audit-pg \
    psql -U "$AUDIT_PG_USER" -d "$AUDIT_PG_DB" -c \
    "SELECT 'tables:' AS k, count(*) FROM pg_tables WHERE schemaname='public'
     UNION ALL SELECT 'policies:', count(*) FROM pg_policies WHERE schemaname='public'
     UNION ALL SELECT 'indexes:', count(*) FROM pg_indexes WHERE schemaname='public';"

echo "Bootstrap complete."

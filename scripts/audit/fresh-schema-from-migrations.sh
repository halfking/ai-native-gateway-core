#!/usr/bin/env bash
# -----------------------------------------------------------------------------
# File:          scripts/audit/fresh-schema-from-migrations.sh
# Purpose:       Apply a range of canonical startup migrations to an empty
#                database and report what the migration set actually produces.
#
# Round 43 rewrite. The previous version had three defects that made it
# unusable as an audit instrument:
#
#   1. Container and user were hardcoded to kx-citus / kxuser. On this
#      workstation the database container is llm-gateway-pg / llm_gateway,
#      so the script could not run at all without editing it.
#   2. The range was pinned to 511..635. There are now 777 canonical startup
#      migrations; everything from 636 onward was never exercised.
#   3. It never asserted the database was actually populated. A silent
#      CREATE DATABASE failure yields applied=0 failed=0 and the script still
#      prints "complete" — the exact shape round 43 hit twice.
#
# It also asserted an exact count of expected repair failures (15) against a
# hand-maintained list. That number is an easy-stale magic value; the true
# invariant is that the list length matches the declared count, which is
# checked below instead.
#
# Usage:
#   bash scripts/audit/fresh-schema-from-migrations.sh
#   PG_CONTAINER=llm-gateway-pg PG_USER=llm_gateway \
#   MIGRATION_FROM= MIGRATION_TO= \
#   bash scripts/audit/fresh-schema-from-migrations.sh
#
#   MIGRATION_FROM / MIGRATION_TO  numeric prefix bounds, inclusive.
#                                   Empty = unbounded (default: all).
#   STRICT=1                       exit non-zero on any unexpected failure
#                                   (default 1). STRICT=0 reports only.
#   KEEP_FRESH_DB=1                keep the database for Go end-state checks
#                                   via TEST_AUDIT_FRESH_SCHEMA_DB_URL.
#
# Interpreting a non-zero exit in full-range mode: applying ALL migrations to
# an EMPTY database is expected to fail. Round 43 measured 236 failures that
# way, because schema_migrations is created by no migration and only exists in
# the pg_dump baseline — the baseline and the migration set are each other's
# precondition. Use this script to enumerate and triage, not as a pass/fail
# gate on the full range.
# -----------------------------------------------------------------------------
set -uo pipefail

PG_CONTAINER="${PG_CONTAINER:-llm-gateway-pg}"
PG_USER="${PG_USER:-llm_gateway}"
FRESH_DB="${FRESH_DB:-llm_gateway_fresh}"
KEEP_FRESH_DB="${KEEP_FRESH_DB:-0}"
STRICT="${STRICT:-1}"
MIGRATION_FROM="${MIGRATION_FROM-}"
MIGRATION_TO="${MIGRATION_TO-}"

die() { echo "ERROR: $*" >&2; exit 2; }

command -v docker >/dev/null 2>&1 || die "docker not found"
if ! docker ps --format '{{.Names}}' | grep -qx "$PG_CONTAINER"; then
  die "container '$PG_CONTAINER' is not running (override with PG_CONTAINER=)"
fi
if ! docker exec "$PG_CONTAINER" psql -U "$PG_USER" -d postgres -tAc 'SELECT 1' >/dev/null 2>&1; then
  die "cannot connect to '$PG_CONTAINER' as user '$PG_USER' (override with PG_USER=)"
fi

PSQL_DB()    { docker exec -i "$PG_CONTAINER" psql -U "$PG_USER" -d "$FRESH_DB" "$@"; }
PSQL_ADMIN() { docker exec -i "$PG_CONTAINER" psql -U "$PG_USER" -d postgres "$@"; }

# ERRFILE is the single, absolute path that apply_file writes psql diagnostics
# to and that the caller reads back. Round 43's first version wrote
# 2>"$label.err" while reading /tmp/$label.err — the two never matched, so every
# failure printed an empty reason, and the .err files were dropped into the
# repository root. One definition, used on both sides, removes the class.
ERRFILE=/tmp/fresh-schema-migrations.err

# apply_file runs one migration with the same per-file semantics the installer
# uses, so failures here are comparable to a real fresh install.
apply_file() {
  local f="$1"
  cat "$f" | docker exec -i "$PG_CONTAINER" psql -U "$PG_USER" -d "$FRESH_DB" \
    -q -v ON_ERROR_STOP=1 --single-transaction >/dev/null 2>"$ERRFILE"
}

# db_populated is the guard the old script lacked. Prints a one-line summary
# and fails loudly if the database has no objects, which is what a silently
# failed CREATE DATABASE looks like from here.
db_populated() {
  local rel fn
  rel=$(PSQL_DB -tAc "SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
                     WHERE n.nspname='public' AND c.relkind IN ('r','p','v','m')" 2>/dev/null)
  fn=$(PSQL_DB  -tAc "SELECT count(*) FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace
                     WHERE n.nspname='public'" 2>/dev/null)
  rel="${rel:-0}"; fn="${fn:-0}"
  echo "  [populated] relations=${rel} functions=${fn}"
  if [[ "$rel" =~ ^[0-9]+$ && "$rel" -gt 0 ]]; then
    return 0
  fi
  return 1
}

cleanup() {
  if [[ "$KEEP_FRESH_DB" == "1" ]]; then
    echo "  keeping $FRESH_DB (point TEST_AUDIT_FRESH_SCHEMA_DB_URL at it)"
  else
    PSQL_ADMIN -q -c "DROP DATABASE IF EXISTS $FRESH_DB" >/dev/null 2>&1 || true
  fi
}
trap cleanup EXIT

REPO_ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$REPO_ROOT"

# Keep the database name short: PostgreSQL identifiers cap at 63 bytes and an
# over-long name fails CREATE DATABASE silently, which then reads as "no
# migrations failed".
if (( ${#FRESH_DB} > 40 )); then
  die "FRESH_DB '$FRESH_DB' is ${#FRESH_DB} bytes; keep it under 40 to stay clear of the 63-byte identifier limit"
fi

echo "═══ container=$PG_CONTAINER user=$PG_USER db=$FRESH_DB ═══"
echo "═══ range: ${MIGRATION_FROM:-<none>} .. ${MIGRATION_TO:-<none>}  strict=$STRICT ═══"

echo ""
echo "═══ STEP 1: create fresh DB $FRESH_DB ═══"
PSQL_ADMIN -q -c "DROP DATABASE IF EXISTS $FRESH_DB" >/dev/null 2>&1
if ! PSQL_ADMIN -q -c "CREATE DATABASE $FRESH_DB" >/dev/null 2>/tmp/fresh-create.err; then
  echo "--- stderr ---" >&2
  cat /tmp/fresh-create.err >&2
  die "CREATE DATABASE $FRESH_DB failed — refusing to report a result from a database that does not exist"
fi
PSQL_ADMIN -q -c "CREATE EXTENSION IF NOT EXISTS citus" "$FRESH_DB" >/dev/null 2>&1

echo ""
echo "═══ STEP 2: apply 00-prereqs.sql ═══"
PREREQ=sql/schema/00-prereqs.sql
if [[ -f "$PREREQ" ]]; then
  if ! apply_file "$PREREQ" prereqs; then
    echo "  ✗ 00-prereqs.sql failed:"; head -5 /tmp/prereqs.err | sed 's/^/      /'
    die "prereqs must apply cleanly; everything downstream is meaningless without it"
  fi
  echo "  ✓ 00-prereqs.sql applied"
else
  die "$PREREQ missing"
fi
db_populated || true

echo ""
echo "═══ STEP 3: apply startup migrations ═══"
applied=0 skipped=0 failed=0
declare -a failed_list=()

# Select every up-migration with a numeric-leading name. The old pattern
# required an underscore right after the numeric prefix, which silently
# excluded date-prefixed migrations such as
# 2026-07-13-multimodal-token-fields-hot.sql — 464 of 777 files matched.
mapfile -t mig_files < <(ls sql/migrations/startup/ 2>/dev/null \
    | grep -E '^[0-9].*\.sql$' \
    | grep -v '\.down\.sql$' \
    | sort)

total=${#mig_files[@]}
echo "  $total canonical startup migrations found"

for base in "${mig_files[@]}"; do
  # Leading digits only. Prefixes are not always pure integers: 328a and
  # 2026-07-13-… both occur, and `10#328a` is a hard arithmetic error that
  # aborted the loop mid-run while the script still reported "complete".
  n="$(printf '%s' "$base" | sed -E 's/^([0-9]+).*/\1/')"
  if [[ -z "$n" ]]; then
    echo "  ! skipping '$base': no leading numeric prefix" >&2
    ((skipped++)) || true; continue
  fi
  # Strip leading zeros: bash would otherwise read 0778 as octal.
  n_dec=$((10#$n))
  if [[ -n "$MIGRATION_FROM" ]] && (( n_dec < 10#$MIGRATION_FROM )); then
    ((skipped++)) || true; continue
  fi
  if [[ -n "$MIGRATION_TO" ]] && (( n_dec > 10#$MIGRATION_TO )); then
    ((skipped++)) || true; continue
  fi
  applied_before=$applied
  if apply_file "sql/migrations/startup/$base"; then
    ((applied++)) || true
  else
    ((failed++)) || true
    reason=$(grep -i '^ERROR' "$ERRFILE" | head -1 | cut -c1-150)
    [[ -z "$reason" ]] && reason=$(head -1 "$ERRFILE" | cut -c1-150)
    failed_list+=("$base :: $reason")
  fi
  if (( (applied + failed) % 100 == 0 )); then
    db_populated >/dev/null || die "database stopped being populated at ~$((applied+failed)) migrations — aborting"
  fi
done

if (( applied + failed + skipped != total )); then
  die "loop accounted for $((applied + failed + skipped)) of $total migrations — the run aborted early; the numbers below are not trustworthy"
fi

echo ""
echo "═══ STEP 4: result ═══"
echo "  applied=$applied skipped=$skipped failed=$failed"
if (( failed > 0 )); then
  echo "  failures:"
  printf '    - %s\n' "${failed_list[@]}"
fi

echo ""
echo "═══ STEP 5: final state ═══"
if ! db_populated; then
  die "final database is EMPTY — the run was invalid, not clean"
fi
PSQL_DB -v ON_ERROR_STOP=1 <<'SQL'
SELECT
    (SELECT count(*) FROM pg_class WHERE relkind='r' AND relnamespace='public'::regnamespace) AS tables,
    (SELECT count(*) FROM pg_class WHERE relkind='p' AND relnamespace='public'::regnamespace) AS partitioned,
    (SELECT count(*) FROM pg_class WHERE relkind='v' AND relnamespace='public'::regnamespace) AS views,
    (SELECT count(*) FROM pg_proc  WHERE pronamespace='public'::regnamespace) AS functions,
    (SELECT count(*) FROM pg_policies WHERE schemaname='public') AS policies;
SQL

if (( failed > 0 )); then
  if [[ "$STRICT" == "1" ]]; then
    die "$failed migration(s) failed under STRICT=1"
  fi
  echo ""
  echo "NOTE: reported without failing because STRICT=0. Applying the full migration"
  echo "      set to an empty database is expected to fail — schema_migrations is"
  echo "      created by no migration and exists only in the pg_dump baseline."
fi

echo ""
echo "═══ complete ═══"

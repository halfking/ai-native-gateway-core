#!/usr/bin/env bash
# -----------------------------------------------------------------------------
# File:          scripts/audit/run-integration-gate.sh
# Purpose:       Run one integration-gated Go test against a freshly created,
#                disposable PostgreSQL database, and — critically — refuse to
#                report success when the suite merely skipped.
#
# Round 43. The existing .github/workflows/integration-testcontainers-ci.yml
# runs `go test -tags=integration ./...` with only TESTCONTAINERS_* in the
# environment. It injects no database URL at all. Measured consequences:
#
#   * 27 of 67 integration test files gate on a DB URL and therefore SKIP,
#     so a green run can contain zero executed database assertions;
#   * the DB URL is read under FIVE different names across the suite
#     (TEST_DATABASE_URL 43 files, TEST_DB_URL 22, TEST_PG_URL 16,
#     LLM_GATEWAY_PG_URL 11, DATABASE_URL 1), so "inject the env var" is
#     ambiguous unless all of them are set;
#   * at least one integration file gates on nothing and never skips —
#     db/db_migration_538_integration_test.go reads no env var and calls no
#     t.Skip — so it hard-fails without a live database instead of skipping.
#
# The convention this script enforces: TEST_PG_URL and friends must point at a
# DISPOSABLE database. A shared database makes integration gates produce false
# red (one test's leftovers are the next test's precondition violation) and
# false green (a skipped suite reads as a pass).
#
# Usage:
#   bash scripts/audit/run-integration-gate.sh ./db
#   bash scripts/audit/run-integration-gate.sh ./internal/dbx TestFoo
#
# Env:
#   PG_CONTAINER  database container (default llm-gateway-pg)
#   PG_USER       database user     (default llm_gateway)
#   KEEP_GATE_DB=1   keep the database for post-mortem
#   ALLOW_VACUOUS=1  exit 0 even if every test skipped (for auditing; the
#                    summary still reports the skip count loudly)
# -----------------------------------------------------------------------------
set -uo pipefail

PG_CONTAINER="${PG_CONTAINER:-llm-gateway-pg}"
PG_USER="${PG_USER:-llm_gateway}"
# Round 43: the first version built the DSN with no password, so every test
# that actually connected failed with "failed SASL auth" — nine misleading
# test failures that looked like product bugs. Set PG_PASSWORD whenever the
# server requires one; the harness now also verifies the DSN works before
# running any test, so this class fails once, up front, with a clear message.
PG_PASSWORD="${PG_PASSWORD-}"
KEEP_GATE_DB="${KEEP_GATE_DB:-0}"
ALLOW_VACUOUS="${ALLOW_VACUOUS:-0}"

PKG="${1:-}"
TEST_NAME="${2:-}"
die() { echo "ERROR: $*" >&2; exit 2; }

[[ -n "$PKG" ]] || die "usage: $0 <package> [test-name]"
command -v docker >/dev/null 2>&1 || die "docker not found"
docker ps --format '{{.Names}}' | grep -qx "$PG_CONTAINER" \
  || die "container '$PG_CONTAINER' not running (override with PG_CONTAINER=)"
docker exec "$PG_CONTAINER" psql -U "$PG_USER" -d postgres -tAc 'SELECT 1' >/dev/null 2>&1 \
  || die "cannot connect to '$PG_CONTAINER' as '$PG_USER' (override with PG_USER=)"

# Short, unique, and well under PostgreSQL's 63-byte identifier cap. A name
# that exceeds the cap makes CREATE DATABASE fail, after which every statement
# errors and the run looks like "everything failed" for the wrong reason.
GATE_DB="itgate_${$}_${RANDOM}"
GATE_DB="${GATE_DB:0:30}"

PGPORT=$(docker port "$PG_CONTAINER" 5432/tcp 2>/dev/null | head -1 | sed 's/.*://')
[[ -n "$PGPORT" ]] || PGPORT=5432
if [[ -n "$PG_PASSWORD" ]]; then
  GATE_URL="postgresql://${PG_USER}:${PG_PASSWORD}@127.0.0.1:${PGPORT}/${GATE_DB}"
else
  GATE_URL="postgresql://${PG_USER}@127.0.0.1:${PGPORT}/${GATE_DB}"
fi

REPO_ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$REPO_ROOT"

cleanup() {
  if [[ "$KEEP_GATE_DB" == "1" ]]; then
    echo "  keeping $GATE_DB"
  else
    docker exec "$PG_CONTAINER" psql -U "$PG_USER" -d postgres -q \
      -c "DROP DATABASE IF EXISTS $GATE_DB" >/dev/null 2>&1 || true
  fi
}
trap cleanup EXIT

echo "═══ integration gate: $PKG ${TEST_NAME:+(run=$TEST_NAME)} ═══"
echo "═══ disposable db=$GATE_DB container=$PG_CONTAINER ═══"

docker exec "$PG_CONTAINER" psql -U "$PG_USER" -d postgres -q \
  -c "DROP DATABASE IF EXISTS $GATE_DB" >/dev/null 2>&1
if ! docker exec "$PG_CONTAINER" psql -U "$PG_USER" -d postgres -q \
     -c "CREATE DATABASE $GATE_DB" >/dev/null 2>/tmp/itgate-create.err; then
  echo "--- stderr ---" >&2; cat /tmp/itgate-create.err >&2
  die "CREATE DATABASE $GATE_DB failed — refusing to run tests against a database that does not exist"
fi
docker exec "$PG_CONTAINER" psql -U "$PG_USER" -d "$GATE_DB" -q \
  -c "CREATE EXTENSION IF NOT EXISTS citus" >/dev/null 2>&1

# Apply prereqs so citus_columnar exists; without it every
# `SET default_table_access_method = columnar` in a migration under test fails.
PREREQ=sql/schema/00-prereqs.sql
if [[ -f "$PREREQ" ]]; then
  cat "$PREREQ" | docker exec -i "$PG_CONTAINER" psql -U "$PG_USER" -d "$GATE_DB" \
    -q -v ON_ERROR_STOP=1 --single-transaction >/dev/null 2>/tmp/itgate-prereq.err
  if [[ $? -ne 0 ]]; then
    echo "  ✗ 00-prereqs.sql failed:"; head -4 /tmp/itgate-prereq.err | sed 's/^/      /'
    die "prereqs must apply cleanly; without extensions the gate proves nothing"
  fi
fi

# Apply the baseline as well. Round 43 established that schema_migrations is
# created by NO migration and exists only in the pg_dump baseline — and the
# db.ensure*() family stamps its migration number into that table. With prereqs
# alone, nine ./db tests failed at `relation "public.schema_migrations" does not
# exist`, which reads like a product defect but is a bootstrap hole. Applying
# 01-schema.sql gives the gate a realistic starting schema.
BASELINE=sql/schema/01-schema.sql
if [[ -f "$BASELINE" ]]; then
  cat "$BASELINE" | docker exec -i "$PG_CONTAINER" psql -U "$PG_USER" -d "$GATE_DB" \
    -q -v ON_ERROR_STOP=1 --single-transaction >/dev/null 2>/tmp/itgate-baseline.err
  if [[ $? -ne 0 ]]; then
    echo "  ✗ 01-schema.sql failed:"; head -4 /tmp/itgate-baseline.err | sed 's/^/      /'
    die "baseline must apply cleanly; the db.ensure*() family needs schema_migrations"
  fi
  echo "  ✓ baseline applied"
fi

# Verify the DSN the tests will actually use. Without this, a wrong DSN
# surfaces as N unrelated per-test failures instead of one clear error.
if ! docker exec "$PG_CONTAINER" psql -U "$PG_USER" -d "$GATE_DB" -tAc 'SELECT 1' >/dev/null 2>&1; then
  die "cannot connect to the disposable database over TCP as '$PG_USER'. "\
"Set PG_PASSWORD if the server requires one (the in-container psql above uses a "\
"local socket, so it can succeed while TCP auth fails)."
fi
# Confirm the URL form the tests will parse actually connects.
if ! psql "$GATE_URL" -tAc 'SELECT 1' >/dev/null 2>&1; then
  die "the generated DSN cannot authenticate: $GATE_URL (redacted). Set PG_PASSWORD if required."
fi

# Population assertion: a test that "ran" against an empty database has not
# proven anything. Same reasoning that caught citus_columnar's schema error.
RELS=$(docker exec "$PG_CONTAINER" psql -U "$PG_USER" -d "$GATE_DB" -tAc \
  "SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
   WHERE n.nspname='public' AND c.relkind IN ('r','p','v','m')" 2>/dev/null || echo 0)
echo "  [populated] relations=${RELS:-0}"
if ! [[ "${RELS:-0}" =~ ^[0-9]+$ ]] || (( RELS <= 0 )); then
  die "disposable database is EMPTY after prereqs — the run would be vacuous"
fi

# Inject every name the suite reads. A one-name injection silently skips the
# 22 files that read TEST_DB_URL and the 16 that read TEST_PG_URL.
echo ""
echo "── running ──"
RUN_LOG=/tmp/itgate-run.log
env \
  TEST_PG_URL="$GATE_URL" \
  TEST_DATABASE_URL="$GATE_URL" \
  TEST_DB_URL="$GATE_URL" \
  LLM_GATEWAY_PG_URL="$GATE_URL" \
  DATABASE_URL="$GATE_URL" \
  go test -tags=integration -count=1 -v -timeout 20m ${TEST_NAME:+-run "$TEST_NAME"} "$PKG" \
  > "$RUN_LOG" 2>&1
TEST_RC=$?

NPASS=$(grep -cE "^\s*--- PASS" "$RUN_LOG")
NSKIP=$(grep -cE "^\s*--- SKIP" "$RUN_LOG")
NFAIL=$(grep -cE "^\s*--- FAIL" "$RUN_LOG")

echo ""
echo "═══ summary ═══"
echo "  exit=$TEST_RC  PASS=$NPASS  SKIP=$NSKIP  FAIL=$NFAIL"
grep -E "^\s*--- FAIL" "$RUN_LOG" | head -10
if (( NFAIL > 0 )); then
  # In `go test -v` the assertion detail is printed BEFORE the "--- FAIL:"
  # marker. Grabbing context after it yields an empty report, which makes a
  # gate that says FAIL without saying why.
  echo "  failing test output (context before each --- FAIL marker):"
  grep -B 14 "^\s*--- FAIL" "$RUN_LOG" | grep -vE "testcontainers-go -|Server Version|API Version|^--$" | tail -40
fi

# The core distinction the repo's discipline demands: "all green" is not the
# same claim as "it actually ran".
if (( NPASS == 0 )); then
  echo ""
  echo "✗ VACUOUS RUN: 0 tests passed."
  if (( NSKIP > 0 )); then
    echo "  $NSKIP test(s) skipped. This is NOT evidence of correctness."
  fi
  if [[ "$ALLOW_VACUOUS" == "1" ]]; then
    echo "  ALLOW_VACUOUS=1 — continuing anyway."
  else
    exit 3
  fi
elif (( NSKIP > 0 )); then
  echo ""
  echo "⚠ $NSKIP test(s) skipped alongside $NPASS passing. Report these as"
  echo "  'green with skips', not as full green."
fi

exit "$TEST_RC"

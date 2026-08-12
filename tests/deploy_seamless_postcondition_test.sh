#!/usr/bin/env bash
# =====================================================================
# tests/deploy_seamless_postcondition_test.sh — offline unit test for
# db-changelog.sh's POST_CONDITION assertion mechanism (rule 11 §6
# audit, round 3: 2026-08-13 P0 silent-fail defence).
#
# Validates:
#   1. _db_extract_postcondition parses -- POST_CONDITION: lines from
#      the first 50 lines of a .sql file, ignoring comments that
#      don't match the marker.
#   2. Migrations without POST_CONDITION run zero assertions.
#   3. Migrations whose POST_CONDITION returns no rows trigger the
#      silent-fail abort path.
#   4. Migrations whose POST_CONDITION returns rows pass cleanly.
#
# Tests 1-4 run offline (no PG needed). Tests 5-6 require a live PG
# reachable via LIVE_PG_DSN and run only when that variable is set.
#
# Invocation:
#   bash tests/deploy_seamless_postcondition_test.sh             # offline only
#   LIVE_PG_DSN='postgres://u:p@host:5432/db' \
#     bash tests/deploy_seamless_postcondition_test.sh             # offline + live
#
# Exit 0 on success, non-zero on first failed assertion.
# =====================================================================

set -uo pipefail

# Allow caller to override LIB path (used by CI smoke test that
# runs this script on a remote box where the repo path is not
# the standard layout).
LIB="${LIB_OVERRIDE:-}"
if [[ -z "$LIB" ]]; then
  ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
  LIB="$ROOT/scripts/deploy-lib/db-changelog.sh"
fi

# Source the lib (functions only, no SSH). The deploy lib's
# _deploy_remote_psql_script + _deploy_verify_ssh + _db_log are
# stubbed below to avoid SSH dependency.
# shellcheck disable=SC1090
source "$LIB"
set +e  # db-changelog.sh has `set -euo pipefail`; turn -e back off so test assertions can exit non-zero without killing the script.

# Override _deploy_verify_ssh + _db_log to run locally without SSH
# and to capture stdout for assertion. This keeps the test offline.
_deploy_verify_ssh() {
  local _ssh_cmd=$1
  shift
  # Strip the _psql wrapper that the deploy lib would normally inject;
  # we run psql directly. The shift removes the wrapper, $@ contains
  # the actual psql command line.
  eval "$@" 2>&1
}
_db_log()  { printf '[db-log] %s\n' "$*" >&2; }
_db_warn() { printf '[db-warn] %s\n' "$*" >&2; }
_db_err()  { printf '[db-err] %s\n' "$*" >&2; }

# For live tests, override _deploy_remote_psql_script to use the
# caller-provided LIVE_PG_DSN. Set _PSQL_BIN to whatever psql variant
# is available (native or docker).
if [[ -n "${LIVE_PG_DSN:-}" ]]; then
  if command -v psql >/dev/null 2>&1; then
    _PSQL_BIN="psql"
  elif command -v docker >/dev/null 2>&1; then
    _PSQL_BIN="docker run --rm --network host postgres:17-alpine psql"
  else
    echo "FAIL: LIVE_PG_DSN set but neither psql nor docker found" >&2
    exit 1
  fi
  _deploy_remote_psql_script() {
    cat <<REMOTE
_psql() {
  ${_PSQL_BIN} "\${LIVE_PG_DSN}" "\$@"
}
REMOTE
  }
fi

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

# --- Test 1: extract POST_CONDITION from a file with 1 assertion ---
cat >"$TMP/one_assertion.sql" <<'SQL'
-- Migration header comment
-- POST_CONDITION: SELECT 1 FROM pg_class WHERE relname = 'pg_class'
BEGIN;
SELECT 1;
SQL

result=$(_db_extract_postcondition "$TMP/one_assertion.sql")
expected="SELECT 1 FROM pg_class WHERE relname = 'pg_class'"
if [[ "$result" != "$expected" ]]; then
  echo "FAIL test1: extract expected '$expected' got '$result'" >&2
  exit 1
fi
echo "PASS test1: extract single POST_CONDITION"

# --- Test 2: extract 3 POST_CONDITION lines from one file ---
cat >"$TMP/three_assertions.sql" <<'SQL'
-- POST_CONDITION: SELECT 1 FROM pg_class WHERE relname = 'pg_class'
-- POST_CONDITION: SELECT count(*) FROM information_schema.tables WHERE table_name='pg_class'
-- POST_CONDITION: SELECT 1
BEGIN;
SELECT 1;
SQL

result=$(_db_extract_postcondition "$TMP/three_assertions.sql" | wc -l | tr -d ' ')
if [[ "$result" != "3" ]]; then
  echo "FAIL test2: expected 3 lines, got '$result'" >&2
  exit 1
fi
echo "PASS test2: extract 3 POST_CONDITION lines"

# --- Test 3: migration without POST_CONDITION returns empty ---
cat >"$TMP/no_assertion.sql" <<'SQL'
-- This migration has no POST_CONDITION.
-- Just a normal header.
BEGIN;
SELECT 1;
SQL

result=$(_db_extract_postcondition "$TMP/no_assertion.sql")
if [[ -n "$result" ]]; then
  echo "FAIL test3: expected empty, got '$result'" >&2
  exit 1
fi
echo "PASS test3: no POST_CONDITION returns empty"

# --- Test 4: comments that LOOK like POST_CONDITION but aren't ---
cat >"$TMP/false_positives.sql" <<'SQL'
-- Some text mentioning POST_CONDITION without the marker
-- A line about: -- POST_CONDITION: should not match without colon-space
-- but with colons: --POST_CONDITION:foo  should also not match (no space)
-- the actual marker line:
-- POST_CONDITION: SELECT 1
BEGIN;
SELECT 1;
SQL

result=$(_db_extract_postcondition "$TMP/false_positives.sql" | wc -l | tr -d ' ')
if [[ "$result" != "1" ]]; then
  echo "FAIL test4: expected 1 (the real marker), got '$result'" >&2
  _db_extract_postcondition "$TMP/false_positives.sql" | head -5 >&2
  exit 1
fi
echo "PASS test4: false-positive filtering works"

if [[ -z "${LIVE_PG_DSN:-}" ]]; then
  echo ""
  echo "OFFLINE TESTS PASSED (4/4). Set LIVE_PG_DSN to run live tests 5-6."
  exit 0
fi

# --- Test 5: live run on a real PG — PASS path ---
result=$(_db_run_postcondition "$TMP/one_assertion.sql" "fake-ssh" "$TMP/env" 2>&1)
rc=$?
if [[ "$rc" != "0" ]]; then
  echo "FAIL test5: PASS path returned $rc" >&2
  echo "$result" >&2
  exit 1
fi
if ! grep -q "assert" <<<"$result"; then
  echo "FAIL test5: expected 'assert' line in deploy log output" >&2
  echo "$result" >&2
  exit 1
fi
echo "PASS test5: PASS path runs assertions and returns 0"

# --- Test 6: live run on a real PG — FAIL path ---
cat >"$TMP/will_fail.sql" <<'SQL'
-- POST_CONDITION: SELECT 1 FROM pg_class WHERE relname = 'nonexistent_xyz_abc'
BEGIN;
SELECT 1;
SQL

result=$(_db_run_postcondition "$TMP/will_fail.sql" "fake-ssh" "$TMP/env" 2>&1)
rc=$?
if [[ "$rc" != "1" ]]; then
  echo "FAIL test6: FAIL path expected rc=1, got $rc" >&2
  echo "$result" >&2
  exit 1
fi
if ! grep -q "POST_CONDITION FAILED" <<<"$result"; then
  echo "FAIL test6: expected 'POST_CONDITION FAILED' in output" >&2
  echo "$result" >&2
  exit 1
fi
echo "PASS test6: FAIL path returns 1 and surfaces the error"

echo ""
echo "ALL 6 TESTS PASSED"

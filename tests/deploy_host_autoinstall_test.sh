#!/usr/bin/env bash
# Opt-in end-to-end test of the host auto-install path (2026-10-11).
#
# The contract suite (tests/deploy_host_first_contract_test.sh) locks the
# discovery ORDER with mocks and runs everywhere. This test goes one level
# deeper: it runs the REAL dl_install_redis / dl_install_postgres code from
# scripts/deploy-local.sh — source builds of Redis and PostgreSQL 17 +
# pgcolumnar — against an ISOLATED shared root, then proves the running
# services answer and the columnar table AM is usable.
#
# It is opt-in because it needs network (source tarballs) and several CPU
# minutes for the PG build. Enable with:
#
#   DL_AUTOINSTALL_TEST=1 bash tests/deploy_host_autoinstall_test.sh
#
# Everything lands under a mktemp dir (KAIXUAN_ROOT is overridden), ports are
# non-default (55432 / 56380) so a real dev machine's services are untouched,
# and both services are stopped + the dir removed on exit.
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

if [[ "${DL_AUTOINSTALL_TEST:-0}" != 1 ]]; then
  printf 'SKIP deploy_host_autoinstall_test (set DL_AUTOINSTALL_TEST=1 to run the real source builds)\n'
  exit 0
fi

source "$ROOT/scripts/deploy-local-lib.sh"

TMP=$(mktemp -d -t kx-autoinstall.XXXXXX)
export KAIXUAN_ROOT="$TMP"
export LLM_GATEWAY_PG_PORT=55432
export LLM_GATEWAY_REDIS_PORT=56380
export LLM_GATEWAY_PG_DATABASE=llm_gateway_ai_test
PG_PREFIX="$TMP/pg"
RUNNING_REDIS=0
RUNNING_PG=0

fail() { printf 'FAIL %s\n' "$*" >&2; exit 1; }
pass() { printf 'PASS %s\n' "$*"; }
log()  { printf '[autoinstall-test] %s\n' "$*"; }
warn() { printf '[autoinstall-test] warning: %s\n' "$*" >&2; }

cleanup() {
  local status=$?
  trap - EXIT INT TERM
  (( RUNNING_REDIS )) && "$TMP/redis/bin/redis-cli" -h 127.0.0.1 -p "$LLM_GATEWAY_REDIS_PORT" shutdown nosave >/dev/null 2>&1 || true
  (( RUNNING_PG )) && "$PG_PREFIX/bin/pg_ctl" -D "$TMP/postgres/data" -m fast -w stop >/dev/null 2>&1 || true
  (( status == 0 )) || printf '[autoinstall-test] artifacts kept for debugging under %s\n' "$TMP" >&2
  (( status == 0 )) && rm -rf "$TMP"
  exit "$status"
}
trap cleanup EXIT INT TERM

# The install functions live in the deploy entry point, which must NOT be
# sourced (sourcing it runs the whole deploy). Slice the host-install section
# out of the real file and load just that. The markers are the section header
# comment and the next top-level function, so the slice cannot silently rot:
# if either marker moves, the guard below fails the test.
sed -n '/^# Host-native service installation/,/^ensure_resources() {/p' \
  "$ROOT/scripts/deploy-local.sh" | sed '$d' > "$TMP/host-install.inc"
grep -q 'dl_install_postgres()' "$TMP/host-install.inc" \
  || fail 'host-install slice is empty — the section markers in deploy-local.sh moved'
# shellcheck disable=SC1090
source "$TMP/host-install.inc"

log "isolated shared root: $TMP"

log 'installing redis (source build) → '"$TMP/redis"
dl_install_redis || fail 'dl_install_redis failed'
RUNNING_REDIS=1
[[ "$("$TMP/redis/bin/redis-cli" -h 127.0.0.1 -p "$LLM_GATEWAY_REDIS_PORT" ping 2>/dev/null)" == PONG ]] \
  || fail 'auto-installed redis did not answer PING'
pass 'auto-installed redis answers PING on 127.0.0.1:'"$LLM_GATEWAY_REDIS_PORT"

log 'installing postgresql 17 + pgcolumnar (source build) → '"$PG_PREFIX"
dl_install_postgres || fail 'dl_install_postgres failed'
RUNNING_PG=1
PSQL="$PG_PREFIX/bin/psql"
q() { "$PSQL" -h 127.0.0.1 -p "$LLM_GATEWAY_PG_PORT" -U "${USER}" -d "$LLM_GATEWAY_PG_DATABASE" -X -Atqc "$1"; }

[[ "$(q 'SHOW server_version_num')" =~ ^17[0-9]{4}$ ]] \
  || fail "expected a PostgreSQL 17.x cluster, got server_version_num=$(q 'SHOW server_version_num' || true)"
pass "auto-installed cluster is PostgreSQL 17.x ($(q 'SHOW server_version'))"

[[ "$(q "SELECT count(*) FROM pg_extension WHERE extname='pgcolumnar'")" == 1 ]] \
  || fail 'pgcolumnar extension not created in the app database'
pass 'pgcolumnar extension is active in the app database'

[[ "$(q "SELECT count(*) FROM pg_am WHERE amname IN ('columnar','pgcolumnar')")" -ge 1 ]] \
  || fail 'no columnar table access method registered (neither columnar nor pgcolumnar)'
pass 'columnar table access method is registered'

# The deliverable is a USABLE columnar store, not just a catalog entry.
# Standalone pgcolumnar registers the AM as `pgcolumnar`; full Citus as
# `columnar` — pick whichever this install exposes.
am_name=$(q "SELECT amname FROM pg_am WHERE amname IN ('columnar','pgcolumnar') ORDER BY amname LIMIT 1")
q "CREATE TABLE _ai_probe(id int, note text) USING $am_name" || fail "CREATE TABLE ... USING $am_name failed"
q "INSERT INTO _ai_probe SELECT g, 'row '||g FROM generate_series(1,1000) g" || fail 'insert into columnar table failed'
[[ "$(q 'SELECT count(*) FROM _ai_probe')" == 1000 ]] || fail 'columnar table lost rows'
q 'DROP TABLE _ai_probe' || true
pass 'columnar table accepts DDL/DML (1000 rows round-trip)'

printf 'ALL AUTO-INSTALL E2E TESTS PASSED\n'

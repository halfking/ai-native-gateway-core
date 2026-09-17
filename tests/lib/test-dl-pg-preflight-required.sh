#!/usr/bin/env bash
# 2026-09-17 audit: contract test for the DL_PG_PREFLIGHT_REQUIRED gate on
# dl_wait_pg_isready (deploy-local Layer 1).
#
# House rule (migration 703 lesson): a gate's self-test must EXECUTE the real
# guard, not grep the source. The live function body is extracted from
# scripts/deploy-local-lib.sh and run against stubbed clock/probe commands.
#
# Contract under test:
#   DL_PG_PREFLIGHT_REQUIRED unset/0 (default, backward compat):
#     - probe timeout   -> warn + rc=1; the caller survives (deploy-local.sh
#       wraps with `|| true` on purpose)
#     - unparseable DSN -> warn + rc=0
#   DL_PG_PREFLIGHT_REQUIRED=1 (fail-closed; enable after 245 preprod):
#     - probe timeout   -> die: exits nonzero with the fail-closed message
#     - unparseable DSN -> die
#     - healthy PG      -> rc=0 (fail-closed must never block a good deploy)
#
# Stubbing note: the function calls `date +%s` inside command substitutions,
# and mutations inside $(...) are invisible to the parent shell. The clock
# therefore advances via a temp-file counter, never a shell variable.
set -uo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/../.."

LIB=scripts/deploy-local-lib.sh
FN=$(sed -n '/^dl_wait_pg_isready()/,/^}/p' "$LIB")
if [[ -z "$FN" || "$FN" != *DL_PG_PREFLIGHT_REQUIRED* ]]; then
  echo "FAIL: dl_wait_pg_isready not found in $LIB or gate env missing from it"; exit 1
fi
export FN_SRC="$FN"

PASS=0; FAIL=0
tcase() { # tcase <name> <want-rc> <want-pattern> <DSN> <required 0|1> <date-mode> <psql-ok-on-call>
  local name="$1" want_rc="$2" want_pat="$3" dsn="$4" req="$5" date_mode="$6" psql_ok="$7"
  local out rc CLOCK
  CLOCK=$(mktemp); : > "$CLOCK"; export CLOCKFILE="$CLOCK"
  out=$(DATE_MODE="$date_mode" PSQL_OK_ON="$psql_ok" DL_PG_PREFLIGHT_REQUIRED="$req" DATABASE_URL="$dsn" bash -c '
    set -euo pipefail
    log(){ :; }
    warn(){ printf "WARN: %s\n" "$*" >&2; }
    die(){ printf "error: %s\n" "$*" >&2; exit 1; }
    _dl_have(){ [[ "$1" == "psql" ]]; }
    DL_DOCKER=0
    T0=1000000000
    date(){ local n; n=$(cat "$CLOCKFILE" 2>/dev/null || echo 0); n=$((n+1)); printf "%s" "$n" > "$CLOCKFILE"; if (( n == 1 )); then printf "%s" "$T0"; elif [[ "$DATE_MODE" == slow ]]; then printf "%s" "$((T0 + n * 2))"; else printf "%s" "$((T0 + 91))"; fi; }
    sleep(){ :; }
    PCALL=0
    psql(){ PCALL=$((PCALL+1)); [[ "$PCALL" -ge "$PSQL_OK_ON" ]]; }
    eval "$FN_SRC"
    dl_wait_pg_isready
  ' 2>&1 ); rc=$?
  rm -f "$CLOCK"
  if [[ "$rc" != "$want_rc" ]]; then
    echo "FAIL $name: rc=$rc want=$want_rc"; printf '%s\n' "$out" | tail -3; FAIL=$((FAIL+1)); return
  fi
  if [[ -n "$want_pat" ]] && ! grep -q "$want_pat" <<<"$out"; then
    echo "FAIL $name: pattern '$want_pat' missing in:"; printf '%s\n' "$out" | tail -3; FAIL=$((FAIL+1)); return
  fi
  echo "PASS $name"; PASS=$((PASS+1))
}

DSN_OK="postgresql://u:p@127.0.0.1:59999/llm_gateway"

tcase "default(0): timeout -> warn rc=1, caller survives" 1 "PG pre-flight timed out after 90s" "$DSN_OK" 0 fast 999
tcase "required=1: timeout -> die fail-closed"           1 "fail-closed (DL_PG_PREFLIGHT_REQUIRED=1)" "$DSN_OK" 1 fast 999
tcase "required=1: unparseable DSN -> die"               1 "could not parse DATABASE_URL" "not-a-dsn" 1 fast 999
tcase "default(0): unparseable DSN -> skip rc=0"         0 "could not parse DATABASE_URL; skipping" "not-a-dsn" 0 fast 999
tcase "required=1: healthy PG on 2nd probe -> rc=0"      0 "" "$DSN_OK" 1 slow 2

# Source-shape guards: the fail-closed marker stays greppable for operators,
# and start_instance keeps its `|| true` (warn-mode) call shape.
grep -q "fail-closed (DL_PG_PREFLIGHT_REQUIRED=1)" "$LIB" || { echo "FAIL: fail-closed marker missing from $LIB"; FAIL=$((FAIL+1)); }
grep -q "dl_wait_pg_isready || true" scripts/deploy-local.sh || { echo "FAIL: start_instance no longer wraps the pre-flight"; FAIL=$((FAIL+1)); }

echo; echo "RESULT: PASS=$PASS FAIL=$FAIL"
[[ "$FAIL" == "0" ]]

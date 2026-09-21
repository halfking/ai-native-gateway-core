#!/usr/bin/env bash
# R37 self-audit behavioral pin: dl_wait_pg_isready's DSN parse guard must
# actually fire. The original `if ! user=$(sed …) || ! …` chain was dead code
# (sed exits 0 on no-match), so unparseable DSNs burned the full 90s probing
# nothing — per start attempt, up to 4x on the cutover path. A good DSN
# parses; a DSN missing any of user/host/port/db must return 0 (advisory
# skip) within seconds, not probe for 90s.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
LIB="$ROOT_DIR/scripts/deploy-local-lib.sh"

bash -n "$LIB"

run_preflight() {
  # shellcheck disable=SC1090
  timeout 20 bash -c '
    set -euo pipefail
    log() { printf "L:%s\n" "$*"; }
    warn() { printf "W:%s\n" "$*"; }
    export DL_DOCKER=0
    export DATABASE_URL="$1"
    source "'"$LIB"'"
    dl_wait_pg_isready
  ' _ "$1"
}

fail() { printf 'FAIL: %s\n' "$*" >&2; exit 1; }

start=$(date +%s)

# 1. DSN without an explicit port → the guard must trip (psql absent on most
#    CI runners would otherwise /dev/tcp-probe "host-nop" until timeout).
out1="$(run_preflight 'postgres://user@host-nop/db' || true)"
elapsed1=$(( $(date +%s) - start ))
grep -q 'could not parse DATABASE_URL' <<<"$out1" || fail "unparseable DSN (no port) did not trip the guard, output: $out1"
if (( elapsed1 >= 15 )); then fail "guard burned ${elapsed1}s on an unparseable DSN (dead-guard regression)"; fi

# 2. A well-formed DSN against a dead address must NOT trip the guard — it
#    probes until timeout and exits 1 (advisory). Bounded: 90s budget, but a
#    fast-fail connection attempt ends the loop quickly when psql is missing
#    and /dev/tcp refuses. We only assert the guard did NOT fire.
out2="$(run_preflight 'postgresql://u:p@127.0.0.1:59999/db?sslmode=disable' || true)"
if grep -q 'could not parse DATABASE_URL' <<<"$out2"; then
  fail "well-formed DSN wrongly tripped the parse guard: $out2"
fi
if grep -q 'ready after' <<<"$out2"; then
  fail "nothing listens on 127.0.0.1:59999 yet preflight reported ready: $out2"
fi

echo 'deploy-local-lib preflight parse-guard test: PASS'

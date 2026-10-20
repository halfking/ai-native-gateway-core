#!/usr/bin/env bash
# -----------------------------------------------------------------------------
# File:          scripts/audit/verify-stop-write-interlock-realdb.sh
# Purpose:       Run the S4 stop-write interlock gate against a REAL database
#                and prove the gate can actually turn RED.
#
# Why this exists (2026-10-20, audit round B4):
#
#   The gate admin/request_logs_stop_write_interlock_test.go asserts the
#   semantics of settings.storage.request_logs_write_enabled — including
#   "no row in settings_kv ⇒ the code default". G-0 flipped that default
#   from true to false, and this gate's positive control still asserted the
#   OLD value.
#
#   Nobody noticed, because the gate is SKIPPED unless TEST_DATABASE_URL is
#   set: "green in the normal CI form" said nothing about the assertions
#   inside it. With a DSN it was RED (measured 2026-10-20:
#   "库里没有该行时应当取默认 true……实测 false").
#
#   This script is the instrument that stops the same thing happening again:
#     1. run the gate against a real database — expect GREEN,
#     2. flip the accessor's fallback back to true — expect RED,
#     3. restore the source (checksum-verified) — expect GREEN again.
#
#   The mutation is applied to Go source and restored from a backup copy
#   whose checksum is verified byte-for-byte, so a failed run cannot leave
#   the repository dirty.
#
# Usage:
#   bash scripts/audit/verify-stop-write-interlock-realdb.sh
#   PG_CONTAINER=llm-gateway-pg PG_USER=llm_gateway bash ...
#
# Env:
#   PG_CONTAINER   container holding the database  (default llm-gateway-pg)
#   PG_USER       database role                   (default llm_gateway)
#   PG_PASSWORD   password for the TCP DSN       (default: read from the container)
#   PG_HOST_PORT  host port for the container     (default: auto-detect)
#
# Exit code: 0 only if the baseline is green AND the mutation turned it red
# AND the restore is byte-identical. A gate that stays green under mutation
# is a FAILED audit, not a pass.
# -----------------------------------------------------------------------------
set -uo pipefail

PG_CONTAINER="${PG_CONTAINER:-llm-gateway-pg}"
PG_USER="${PG_USER:-llm_gateway}"

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$REPO_ROOT" || { echo "ERROR: cannot enter repo root" >&2; exit 2; }

command -v docker >/dev/null 2>&1 || { echo "ERROR: docker not found in PATH" >&2; exit 2; }
docker ps --format '{{.Names}}' | grep -qx "$PG_CONTAINER" \
  || { echo "ERROR: container '$PG_CONTAINER' not running" >&2; exit 2; }

# The Go toolchain is not always on PATH in non-interactive shells.
if ! command -v go >/dev/null 2>&1; then
  for cand in "$HOME/sdk/go/bin" /opt/homebrew/bin /usr/local/go/bin; do
    [ -x "$c/go" ] && export PATH="$cand:$PATH" && break
  done
fi
command -v go >/dev/null 2>&1 || { echo "ERROR: go not found in PATH" >&2; exit 2; }

# Password: prefer the explicit env, else ask the container.
if [[ -z "${PG_PASSWORD:-}" ]]; then
  PG_PASSWORD=$(docker inspect "$PG_CONTAINER" \
    --format '{{range .Config.Env}}{{println .}}{{end}}' 2>/dev/null \
    | sed -n 's/^POSTGRES_PASSWORD=//p' | head -1)
fi
[[ -n "${PG_PASSWORD:-}" ]] || {
  echo "ERROR: no PG_PASSWORD (set it, or let the container be inspected)" >&2; exit 2; }

PG_HOST_PORT="${PG_HOST_PORT:-$(docker port "$PG_CONTAINER" 5432/tcp 2>/dev/null | head -1 | sed 's/.*://')}"
[[ -n "$PG_HOST_PORT" ]] || PG_HOST_PORT=5432

DSN="postgres://${PG_USER}:${PG_PASSWORD}@127.0.0.1:${PG_HOST_PORT}/postgres?sslmode=disable"

# The gate needs a server where it can CREATE DATABASE (it builds its own
# throwaway fixture), so it is pointed at the maintenance database.
GATE='TestStopWriteInterlock_UnclassifiedReadersKeepTheFlagOn'
GATE_PKG='./admin/'

run_gate() {  # run_gate <label>
  TEST_DATABASE_URL="$DSN" go test "$GATE_PKG" -run "$GATE" -count=1 -v 2>&1
}

verdict() {  # verdict <output> -> PASS | FAIL | UNKNOWN
  if echo "$1" | grep -qE '^--- FAIL:|^FAIL'; then echo FAIL
  elif echo "$1" | grep -qE '^--- PASS:|^ok |^PASS'; then echo PASS
  else echo UNKNOWN; fi
}

PASS=0
FAIL=0
report() {  # report <label> <expected> <actual>
  if [[ "$3" == "$2" ]]; then
    printf '   %-56s %s\n' "$1" "OK ($2 as expected)"
    PASS=$((PASS+1))
  else
    printf '   %-56s %s\n' "$1" "MISMATCH (expected $2, got $3)"
    FAIL=$((FAIL+1))
  fi
}

echo "== 1/3 baseline: the interlock gate is green against a real database =="
BASE_OUT=$(run_gate baseline)
BASE_V=$(verdict "$BASE_OUT")
echo "$BASE_OUT" | grep -E '^(=== RUN|--- (PASS|FAIL)|    --- (PASS|FAIL))' | sed 's/^/   | /'
report "baseline gate" PASS "$BASE_V"
if [[ "$BASE_V" != "PASS" ]]; then
  echo "ERROR: baseline is not green — the gate itself is broken, not the mutation" >&2
  echo "$BASE_OUT" | tail -40 >&2
  exit 2
fi

echo
echo "== 2/3 mutation: flip the spec's Default back to true =="
# ★ WHICH source is load-bearing (measured 2026-10-20, not guessed):
#   the gate wires the settings registry to a real database, so the read
#   path is getPlatformBool → Global.EffectiveValue, and EffectiveValue's
#   step 3 falls back to **the spec's Default** — NOT to the accessor's
#   literal. Mutating the accessor's fallback (key_request_logs_write_enabled.go)
#   therefore leaves this gate GREEN: that literal only serves the
#   "Global == nil / spec not registered" path, which this gate does not
#   exercise. The accessor's own fallback is pinned by a different gate
#   (settings/key_request_logs_write_enabled_test.go, no DSN needed).
#   ⇒ The mutation below targets the spec, which IS on this gate's path.
SPEC="settings/spec_storage.go"
BACKUP="/tmp/s4ilock-spec.$$.sql"
cp "$SPEC" "$BACKUP"
BEFORE_SUM=$(shasum -a 256 "$SPEC" | awk '{print $1}')

python3 - "$SPEC" <<'PY'
import sys
p = sys.argv[1]
s = open(p, encoding="utf-8").read()
old = """			Key:             "storage.request_logs_write_enabled",
			Type:            TypeBool,
			Scope:           ScopePlatform,
			Category:        CategoryStorage,
			Default:         false,"""
new = """			Key:             "storage.request_logs_write_enabled",
			Type:            TypeBool,
			Scope:           ScopePlatform,
			Category:        CategoryStorage,
			Default:         true, // MUTATION: default flipped back to true"""
if s.count(old) != 1:
    sys.stderr.write("mutation anchor found %d times, want exactly 1\n" % s.count(old))
    sys.exit(3)
open(p, "w", encoding="utf-8").write(s.replace(old, new, 1))
PY
if [[ $? -ne 0 ]]; then
  echo "   [M1] mutation could not be applied (anchor moved) — restoring and reporting UNVERIFIED"
  cp "$BACKUP" "$SPEC"
  FAIL=$((FAIL+1))
else
  MUT_OUT=$(run_gate m1)
  MUT_V=$(verdict "$MUT_OUT")
  echo "$MUT_OUT" | grep -E '(--- FAIL|应当取默认|默认语义)' | head -8 | sed 's/^/   | /'
  report "M1 spec Default=true -> RED" FAIL "$MUT_V"
  cp "$BACKUP" "$SPEC"
fi

AFTER_SUM=$(shasum -a 256 "$SPEC" | awk '{print $1}')
if [[ "$BEFORE_SUM" != "$AFTER_SUM" ]]; then
  echo "   [M1] source restored but the checksum differs from the original — INVESTIGATE" >&2
  FAIL=$((FAIL+1))
else
  echo "   [M1] source restored, checksum identical ($AFTER_SUM)"
fi

echo
echo "== 3/3 restore check: the gate is green again =="
RESTORE_OUT=$(run_gate restore)
RESTORE_V=$(verdict "$RESTORE_OUT")
report "after restoring the mutation -> PASS" PASS "$RESTORE_V"

echo
echo "== summary =="
echo "   checks passed: $PASS"
echo "   checks failed: $FAIL"
if [[ "$FAIL" -ne 0 ]]; then
  echo "RESULT: FAIL — a mutation stayed green, or a restore check failed" >&2
  exit 1
fi
echo "RESULT: PASS — baseline green, the mutation turned the gate red, restore verified"
exit 0

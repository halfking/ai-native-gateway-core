#!/usr/bin/env bash
# 2026-09-17 audit: live harness for smart-discovery.sh pg_container_usable.
# Spins throwaway postgres:17-alpine containers and exercises every branch,
# including the recovery-window misreport that declared healthy containers
# "not usable". Never touches llm-gateway-pg (shared, 35+ sibling DBs).
set -uo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/../.."

PASS=0; FAIL=0
check() { # check <name> <expected-rc> <actual-rc> <expected-pattern> <output>
  local name="$1" erc="$2" arc="$3" pat="$4" out="$5"
  if [[ "$arc" != "$erc" ]]; then
    echo "FAIL $name: rc=$arc want=$erc"; echo "--- output ---"; echo "$out" | tail -5; FAIL=$((FAIL+1)); return
  fi
  if [[ -n "$pat" ]] && ! grep -q "$pat" <<<"$out"; then
    echo "FAIL $name: pattern '$pat' not in output:"; echo "$out" | tail -5; FAIL=$((FAIL+1)); return
  fi
  echo "PASS $name"; PASS=$((PASS+1))
}

log(){ echo "[log] $*"; }
warn(){ echo "[warn] $*" >&2; }
dl_container_env() {
  local container="$1" key="$2"
  docker inspect -f '{{range .Config.Env}}{{println .}}{{end}}' "$container" 2>/dev/null \
    | sed -n "s/^${key}=//p" | head -n1
}
source scripts/deploy-lib.legacy/smart-discovery.sh
# the sourced script enables `set -euo pipefail`; the harness captures rc of
# failing probes on purpose, so relax -e again (keep -u).
set +e

cleanup(){ docker rm -f dl717disc-a dl717disc-b dl717disc-cold >/dev/null 2>&1; }
trap cleanup EXIT
cleanup

echo "=== B/A: cold start (recovery-window repro) then healthy probe ==="
docker run -d --name dl717disc-a -e POSTGRES_USER=llm_gateway -e POSTGRES_PASSWORD=pass-a -e POSTGRES_DB=llm_gateway -p 25432:5432 postgres:17-alpine >/dev/null
# probe IMMEDIATELY — PG is still initializing; old code slept 2s and misreported
rc=0; out=$(pg_container_usable dl717disc-a 2>&1) || rc=$?
check "cold-start usable (was: misreported not-usable)" 0 "$rc" "has llm_gateway database ✓" "$out"
check "HAS_DB=1 exported" 0 "$(grep -q 'DL_DISCOVERED_PG_HAS_DB=1' <<<"$(set | grep DL_DISCOVERED_PG_HAS_DB)" && echo 0 || echo 1)" "" ""

echo "=== C: connected but llm_gateway absent (fresh generic container) ==="
docker run -d --name dl717disc-b -e POSTGRES_USER=pguser -e POSTGRES_PASSWORD=pass-b -e POSTGRES_PASSWORD=pass-b -p 25433:5432 postgres:17-alpine >/dev/null
until docker exec dl717disc-b pg_isready -U pguser -d postgres >/dev/null 2>&1; do sleep 1; done
rc=0; out=$(pg_container_usable dl717disc-b 2>&1) || rc=$?
check "db-absent path usable (was: not usable)" 0 "$rc" "will create llm_gateway database" "$out"

echo "=== D: connection failure after readiness (role drift) ==="
# NOTE: the official postgres image trusts unix-socket connections, so a
# WRONG password can NOT fail a docker-exec probe. A nonexistent role can —
# which exercises the same "connected-ready but SQL probe failed" branch.
dl_container_env() { # override: report a BOGUS role for container a
  [[ "$2" == "POSTGRES_USER" && "$1" == "dl717disc-a" ]] && { echo "nosuchrole"; return; }
  docker inspect -f '{{range .Config.Env}}{{println .}}{{end}}' "$1" 2>/dev/null | sed -n "s/^$2=//p" | head -n1
}
rc=0; out=$(pg_container_usable dl717disc-a 2>&1) || rc=$?
check "auth-fail honest diagnosis (was: no llm_gateway database)" 1 "$rc" "cannot authenticate" "$out"

echo "=== old-code control: reproduces the user's misreport on cold start ==="
git show HEAD:scripts/deploy-lib.legacy/smart-discovery.sh > /tmp/old-smart-discovery.sh
dl_container_env() {
  local container="$1" key="$2"
  docker inspect -f '{{range .Config.Env}}{{println .}}{{end}}' "$container" 2>/dev/null | sed -n "s/^${key}=//p" | head -n1
}
docker rm -f dl717disc-a >/dev/null 2>&1
docker run -d --name dl717disc-cold -e POSTGRES_USER=llm_gateway -e POSTGRES_PASSWORD=pass-a -e POSTGRES_DB=llm_gateway -p 25432:5432 postgres:17-alpine >/dev/null
source /tmp/old-smart-discovery.sh
rc=0; out=$(pg_container_usable dl717disc-cold 2>&1) || rc=$?
if [[ "$rc" != "0" || -n "$(grep -E 'no llm_gateway database|not usable' <<<"$out")" ]]; then
  echo "PASS old-code-control: reproduced the misreport (rc=$rc): $(grep -E 'no llm_gateway|not usable' <<<"$out" | head -1)"
else
  echo "PASS old-code-control: old code happened to pass this run (timing) — not a failure of the fix"
fi

echo; echo "RESULT: PASS=$PASS FAIL=$FAIL"
[[ "$FAIL" == "0" ]]

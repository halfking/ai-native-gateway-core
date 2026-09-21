#!/usr/bin/env bash
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
source "$ROOT/scripts/deploy-lib/zero-downtime.sh"

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
fragment="$tmp/active-upstream.conf"
printf 'server 127.0.0.1:8781;\n' >"$fragment"
log="$tmp/commands"
runner() { printf '%s\n' "$1" >>"$log"; bash -c "$1"; }

zd_switch_upstream runner "$fragment" 8782 true true
[[ "$(cat "$fragment")" == 'server 127.0.0.1:8782 max_fails=3 fail_timeout=10s;' ]]

# A validation failure restores the prior fragment.
printf 'server 127.0.0.1:8781;\n' >"$fragment"
if zd_switch_upstream runner "$fragment" 8782 false true; then
  echo 'expected failed nginx validation' >&2
  exit 1
fi
[[ "$(cat "$fragment")" == 'server 127.0.0.1:8781;' ]]

record=$(zd_handoff_record 8781 8782 42 old new)
printf '%s\n' "$record" | python3 -c 'import json,sys; d=json.load(sys.stdin); assert d["handoff_elapsed_ms"] == 42'
printf 'PASS zero_downtime_test\n'

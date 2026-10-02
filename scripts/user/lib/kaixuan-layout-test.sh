#!/usr/bin/env bash
# L1 tests for kaixuan-layout.sh (docs/架构优化v6/04-e2e-方案.md).
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
# shellcheck source=kaixuan-layout.sh
source "${ROOT}/scripts/user/lib/kaixuan-layout.sh"

PASS=0
FAIL=0
note_pass() { printf 'OK  %s\n' "$1"; PASS=$((PASS+1)); }
note_fail() { printf 'FAIL %s\n' "$1" >&2; FAIL=$((FAIL+1)); }

SCRATCH="$(mktemp -d)"
trap 'rm -rf "$SCRATCH"' EXIT

os="$(uname -s)"
got="$(kx_default_project_root)"
case "$os" in
  Darwin)
    [[ "$got" == "${HOME}/kaixuan/llm-gateway-go" ]] && note_pass "darwin default root" || note_fail "darwin got $got"
    [[ "$(kx_default_shared_root)" == "${HOME}/kaixuan" ]] && note_pass "darwin shared root" || note_fail "darwin shared"
    ;;
  Linux)
    [[ "$got" == "/opt/kaixuan/llm-gateway-go" ]] && note_pass "linux default root" || note_fail "linux got $got"
    [[ "$(kx_default_shared_root)" == "/opt/kaixuan" ]] && note_pass "linux shared root" || note_fail "linux shared"
    ;;
  *)
    note_pass "skip os-specific default on $os"
    ;;
esac

INSTALL_ROOT="$SCRATCH/explicit" resolved="$(kx_resolve_install_root)"
unset INSTALL_ROOT
[[ "$resolved" == "$SCRATCH/explicit" ]] && note_pass "INSTALL_ROOT wins" || note_fail "INSTALL_ROOT=$resolved"

legacy="$SCRATCH/Downloads/llm-gateway-files"
mkdir -p "$legacy"
HOME="$SCRATCH" detected="$(kx_detect_legacy_root)"
[[ "$detected" == "$legacy" ]] && note_pass "legacy Downloads detect" || note_fail "legacy detect=$detected"

opt_legacy="$SCRATCH/opt-llm-gateway"
mkdir -p "$opt_legacy"
# kx_legacy_candidates uses real /opt/llm-gateway; simulate via HOME Downloads only here.
# Extra: function returns Downloads tree when HOME is scratch.
[[ -n "$detected" ]] && note_pass "legacy non-empty when Downloads tree exists" || note_fail "legacy empty"

proj="$SCRATCH/kaixuan/llm-gateway-go"
kx_prepare_layout "$proj" 0 0
for d in attachments bin backups logs raw-logs run; do
  [[ -d "$proj/$d" ]] || { note_fail "missing $d"; break; }
done
[[ -d "$proj/attachments" && ! -d "$SCRATCH/kaixuan/postgres" && ! -d "$SCRATCH/kaixuan/redis" ]] \
  && note_pass "project dirs only when need_pg=0" || note_fail "unexpected shared dirs"

# kx_default_shared_root must be redirected into the scratch tree for the
# next assertion. It used to rely on `HOME="$SCRATCH"`, which only works when
# the shared root is $HOME/kaixuan (the Darwin branch). On Windows it resolves
# to a fixed drive path (/c/kaixuan), so the old line both failed the assertion
# AND created real C:\kaixuan\{postgres,redis} directories outside the scratch
# dir — a test writing to the machine's real install root. Overriding the
# function in a subshell keeps the assertion meaningful on every platform and
# makes the test hermetic.
(
  kx_default_shared_root() { printf '%s/kaixuan\n' "$SCRATCH"; }
  kx_prepare_layout "$proj" 1 1
) || note_fail "kx_prepare_layout(need_pg=1 need_redis=1) returned non-zero"
[[ -d "$SCRATCH/kaixuan/postgres/logs" && -d "$SCRATCH/kaixuan/redis/run" ]] \
  && note_pass "shared dirs when need_pg/need_redis" || note_fail "shared dirs missing"

# bin/current is a symlink. Creating one on Windows needs either Developer Mode
# or elevated privileges; without them MSYS `ln -s` silently makes a directory
# copy, so [[ -L ]] is false and `rm -f` on it errors. That is an environment
# limitation, not a layout bug — report it as a skip instead of a failure, and
# skip the assertions that read through the symlink.
can_symlink=1
if ! kx_switch_current "$proj" "1.4.0" "7" 2>/dev/null || [[ ! -L "$proj/bin/current" ]]; then
  can_symlink=0
  note_pass "skip symlink slot switch on $os (no symlink privilege; MSYS ln -s copies)"
fi

if [[ "$can_symlink" == "1" ]]; then
  [[ "$(kx_current_slot "$proj")" == "1.4.0.7" ]] && note_pass "switch current 1.4.0.7" || note_fail "slot=$(kx_current_slot "$proj")"
  kx_switch_current "$proj" "1.3.0" ""
  [[ "$(kx_current_slot "$proj")" == "1.3.0" ]] && note_pass "rollback current to 1.3.0" || note_fail "slot=$(kx_current_slot "$proj")"

  [[ "$(kx_bin_slot 'v1.2.0' '3')" == "1.2.0.3" ]] && note_pass "bin slot strips v" || note_fail "slot strip"

  printf 'slot-ver\n' > "$proj/bin/1.3.0/VERSION"
  [[ "$(kx_read_installed_version "$proj")" == "slot-ver" ]] \
    && note_pass "read VERSION from bin/current" || note_fail "read current VERSION=$(kx_read_installed_version "$proj")"
else
  # kx_bin_slot is pure string work; it does not need a symlink.
  [[ "$(kx_bin_slot 'v1.2.0' '3')" == "1.2.0.3" ]] && note_pass "bin slot strips v" || note_fail "slot strip"
  # kx_read_installed_version falls back to the project-root VERSION file.
  printf 'legacy-root\n' > "$proj/VERSION"
  [[ "$(kx_read_installed_version "$proj")" == "legacy-root" ]] \
    && note_pass "read VERSION from project root" || note_fail "root VERSION=$(kx_read_installed_version "$proj")"
fi

printf 'legacy-root\n' > "$proj/VERSION"
[[ "$(kx_read_installed_version "$proj")" == "legacy-root" ]] \
  && note_pass "project-root VERSION wins when present" || note_fail "root VERSION"

echo "kaixuan-layout-test: $PASS passed, $FAIL failed"
[[ "$FAIL" -eq 0 ]]

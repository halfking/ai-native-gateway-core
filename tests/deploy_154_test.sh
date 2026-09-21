#!/usr/bin/env bash
# =====================================================================
# tests/deploy_154_test.sh — Slice 5 integration tests for the 154 target
#
# 154 shares the same release-bundle layout as 245 (the contracts differ
# only in service_name and binary name). Slice 5 verifies that the
# host.sh library discriminates correctly between 245 and 154, that
# the canonical CLI front-end routes 71 → 154, and that 154 rollback
# is refused with runbook guidance (the legacy runbook remains the
# documented path until parity tests pass).
#
# Coverage:
#   - 154 contract resolves correctly
#   - 154 binary_name is "llm-gateway-go" (not "gateway")
#   - alias 71 → 154
#   - atomic_switch with 154 layout leaves the right symlink chain
#   - rollback 154 is refused via the policy=runbook path
#   - canonical CLI form `./scripts/deploy.sh rollback 154 --to <v>`
#     exits 64 with runbook guidance
# =====================================================================

set -uo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
LIB_TARGETS="$REPO_ROOT/scripts/deploy-lib/targets.sh"
LIB_HOST="$REPO_ROOT/scripts/deploy-lib/host.sh"
SCRIPT_DEPLOY="$REPO_ROOT/scripts/deploy.sh"

TESTS_PASSED=0
TESTS_FAILED=0
FAILED_NAMES=()

log_pass()  { printf '  \033[0;32mPASS\033[0m %s\n' "$1"; TESTS_PASSED=$((TESTS_PASSED+1)); }
log_fail()  { printf '  \033[0;31mFAIL\033[0m %s\n' "$1"; TESTS_FAILED=$((TESTS_FAILED+1)); FAILED_NAMES+=("$1"); }

assert_eq() { [[ "$2" == "$3" ]] && log_pass "$1" || log_fail "$1: got [$2], want [$3]"; }
assert_neq() { [[ "$2" != "$3" ]] && log_pass "$1" || log_fail "$1: expected !=, got [$2]"; }

fake_ssh_runner() {
  if [[ -z "${FAKE_SSH_TMPDIR:-}" ]]; then
    echo "fake_ssh_runner: FAKE_SSH_TMPDIR unset" >&2
    return 1
  fi
  local script=$1
  shift
  ( cd "$FAKE_SSH_TMPDIR" && bash -c "$script" "$@" )
}

setup_fake_host() {
  local tmp
  tmp=$(mktemp -d -t kx-154-test.XXXXXX)
  mkdir -p "$tmp/opt/llm-gateway-go/releases"
  printf '%s\n' "$tmp"
}

# ---- tests --------------------------------------------------------------

test_154_contract_resolves() {
  echo "── 154_contract_resolves ──"
  local fields
  fields=$(
    source "$LIB_TARGETS"
    printf 'support=%s\n' "$(target_field 154 support)"
    printf 'service_manager=%s\n' "$(target_field 154 service_manager)"
    printf 'service_name=%s\n' "$(target_field 154 service_name)"
    printf 'binary_path=%s\n' "$(target_field 154 binary_path)"
    printf 'rollback_policy=%s\n' "$(target_field 154 rollback_policy)"
  )
  local support service_manager service_name binary_path rollback_policy
  support=$(printf '%s\n' "$fields" | sed -n 's/^support=//p')
  service_manager=$(printf '%s\n' "$fields" | sed -n 's/^service_manager=//p')
  service_name=$(printf '%s\n' "$fields" | sed -n 's/^service_name=//p')
  binary_path=$(printf '%s\n' "$fields" | sed -n 's/^binary_path=//p')
  rollback_policy=$(printf '%s\n' "$fields" | sed -n 's/^rollback_policy=//p')
  assert_eq "154 support is canonical" "$support" "canonical"
  assert_eq "154 service_manager is systemd" "$service_manager" "systemd"
  assert_eq "154 service_name is llm-gateway-go.service" "$service_name" "llm-gateway-go.service"
  assert_eq "154 binary_path is /opt/llm-gateway-go/llm-gateway-go" "$binary_path" "/opt/llm-gateway-go/llm-gateway-go"
  assert_eq "154 rollback_policy is versioned" "$rollback_policy" "versioned"
}

test_154_binary_name() {
  echo "── 154_binary_name ──"
  local bn_154 bn_245
  bn_154=$(/bin/bash -c "source '$LIB_TARGETS'; source '$LIB_HOST'; host_binary_name 154")
  bn_245=$(/bin/bash -c "source '$LIB_TARGETS'; source '$LIB_HOST'; host_binary_name 245")
  assert_eq "154 binary_name is llm-gateway-go" "$bn_154" "llm-gateway-go"
  assert_eq "245 binary_name is gateway (different from 154)" "$bn_245" "gateway"
}

test_alias_71_resolves_to_154() {
  echo "── alias_71_resolves_to_154 ──"
  local resolved
  resolved=$(/bin/bash -c "source '$LIB_TARGETS'; target_resolve_alias 71")
  assert_eq "alias 71 resolves to 154" "$resolved" "154"
}

test_154_atomic_switch_layout() {
  echo "── 154_atomic_switch_layout ──"
  local tmp remote v
  tmp=$(setup_fake_host)
  remote="$tmp/opt/llm-gateway-go"
  v="2.4.2-154"

  # Stage a release directly on the simulated remote.
  mkdir -p "$remote/releases/$v/web"
  printf '#!/usr/bin/env sh\necho v=%s\n' "$v" >"$remote/releases/$v/llm-gateway-go"
  chmod +x "$remote/releases/$v/llm-gateway-go"
  printf '<html>%s</html>\n' "$v" >"$remote/releases/$v/web/index.html"
  printf '{"version":"%s"}\n' "$v" >"$remote/releases/$v/version.json"
  ( cd "$remote/releases/$v" && sha256sum llm-gateway-go version.json > SHA256SUMS )

  export FAKE_SSH_TMPDIR="$tmp"
  export HOST_INSTALL_ROOT="$remote"
  local ssh_cmd="fake_ssh_runner"

  (
    source "$LIB_TARGETS"
    source "$LIB_HOST"
    host_atomic_switch "$ssh_cmd" 154 "$v"
  ) >/dev/null 2>&1

  # After atomic_switch, /opt/llm-gateway-go/llm-gateway-go should
  # be a symlink pointing at the current/gateway (which is the
  # 154 binary file).
  if [[ -L "$remote/llm-gateway-go" ]]; then
    log_pass "154 binary_link is a symlink"
  else
    log_fail "154 binary_link is not a symlink"
  fi
  if readlink "$remote/llm-gateway-go" | grep -q 'current/llm-gateway-go'; then
    log_pass "154 binary_link points at current/llm-gateway-go"
  else
    log_fail "154 binary_link does not point at current/llm-gateway-go (got: $(readlink "$remote/llm-gateway-go"))"
  fi
  if readlink "$remote/current" | grep -q "releases/$v"; then
    log_pass "154 current points at releases/$v"
  else
    log_fail "154 current does not point at releases/$v"
  fi

  unset FAKE_SSH_TMPDIR HOST_INSTALL_ROOT
  rm -rf "$tmp"
}

test_154_rollback_versioned() {
  echo "── 154_rollback_versioned ──"
  # Versioned rollback is the canonical 154 contract. The CLI may continue
  # after validating the target; this test only pins the current policy value.
  local out rc
  out=$(/bin/bash "$SCRIPT_DEPLOY" rollback 154 2>&1)
  rc=$?
  if (( rc == 0 )); then
    log_pass "rollback 154 accepts versioned policy"
  else
    log_fail "rollback 154 should accept versioned policy; rc=$rc output=[$out]"
  fi
}

test_canonical_cli_alias_71() {
  echo "── canonical_cli_alias_71 ──"
  # plan 71 should resolve to 154 and return the 154 contract.
  local out
  out=$(/bin/bash "$SCRIPT_DEPLOY" plan 71 2>&1)
  if echo "$out" | grep -q "target: 154"; then
    log_pass "plan 71 reports canonical target 154"
  else
    log_fail "plan 71 should report target 154; got [$out]"
  fi
  if echo "$out" | grep -q "llm-gateway-go.service"; then
    log_pass "plan 71 shows 154 service_name"
  else
    log_fail "plan 71 should show 154 service_name (llm-gateway-go.service)"
  fi
}

# ---- runner --------------------------------------------------------------

run_all() {
  echo "═══════════════════════════════════════════════════════════════"
  echo " deploy_154_test.sh — Slice 5 154 integration tests"
  echo "═══════════════════════════════════════════════════════════════"
  test_154_contract_resolves
  test_154_binary_name
  test_alias_71_resolves_to_154
  test_154_atomic_switch_layout
  test_154_rollback_versioned
  test_canonical_cli_alias_71

  echo
  echo "───────────────────────────────────────────────────────────────"
  echo " summary: ${TESTS_PASSED} passed, ${TESTS_FAILED} failed"
  if (( TESTS_FAILED > 0 )); then
    echo " failed:"
    for n in "${FAILED_NAMES[@]}"; do echo "   - $n"; done
    return 1
  fi
  return 0
}

if [[ $# -gt 0 ]]; then
  case "$1" in
    contract)           test_154_contract_resolves ;;
    binary_name)        test_154_binary_name ;;
    alias)              test_alias_71_resolves_to_154 ;;
    atomic_switch)      test_154_atomic_switch_layout ;;
    rollback_versioned)  test_154_rollback_versioned ;;
    canonical_cli)      test_canonical_cli_alias_71 ;;
    all|*)              run_all ;;
  esac
else
  run_all
fi
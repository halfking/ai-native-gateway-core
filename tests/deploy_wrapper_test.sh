#!/usr/bin/env bash
# =====================================================================
# tests/deploy_wrapper_test.sh — Slice 8 deprecation-wrapper tests
#
# The canonical spec requires deploy/deploy.sh and deploy/rollback.sh
# to be fail-closed wrappers that forward to the canonical CLI rather
# than ship parallel implementations. Slice 8 converts them; this
# harness verifies the forwarding behavior:
#
#   - Both wrappers print a deprecation warning on stderr
#   - They forward positional + flag arguments unchanged
#   - Exit codes match the canonical CLI exit code
#   - Missing canonical CLI exits 64 (fail-closed) with a clear message
#
# The wrappers are exercised with real subprocess invocations because
# the canonical CLI's behavior is environment-sensitive; the tests
# assert observable properties (stdout, stderr, exit code) rather
# than internal bash-state.
# =====================================================================

set -uo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
WRAPPER_DEPLOY="$REPO_ROOT/deploy/deploy.sh"
WRAPPER_ROLLBACK="$REPO_ROOT/deploy/rollback.sh"
CANONICAL="$REPO_ROOT/scripts/deploy.sh"

TESTS_PASSED=0
TESTS_FAILED=0
FAILED_NAMES=()

log_pass()  { printf '  \033[0;32mPASS\033[0m %s\n' "$1"; TESTS_PASSED=$((TESTS_PASSED+1)); }
log_fail()  { printf '  \033[0;31mFAIL\033[0m %s\n' "$1"; TESTS_FAILED=$((TESTS_FAILED+1)); FAILED_NAMES+=("$1"); }

assert_rc()   { [[ "$2" == "$3" ]] && log_pass "$1" || log_fail "$1: rc got [$2], want [$3]"; }
assert_match() { [[ "$3" =~ $2 ]] && log_pass "$1" || log_fail "$1: needle [$2] not in [$3]"; }

# ---- tests --------------------------------------------------------------

test_deploy_wrapper_plan_154() {
  echo "── deploy_wrapper_plan_154 ──"
  local out err rc
  out=$("$WRAPPER_DEPLOY" plan 154 --json 2>/tmp/deploy_err.$$)
  rc=$?
  err=$(cat /tmp/deploy_err.$$)
  rm -f /tmp/deploy_err.$$

  assert_rc "deploy/deploy.sh plan 154 --json exits 0" "$rc" "0"
  assert_match "deploy/deploy.sh plan 154 --json prints the target contract" \
    '"target":"154"' "$out"
  assert_match "deploy/deploy.sh plan 154 --json prints deprecation warning" \
    'deploy/deploy.sh is deprecated' "$err"
}

test_rollback_wrapper_versioned() {
  echo "── rollback_wrapper_versioned ──"
  local out err rc
  out=$("$WRAPPER_ROLLBACK" 154 2>/tmp/rollback_err.$$)
  rc=$?
  err=$(cat /tmp/rollback_err.$$)
  rm -f /tmp/rollback_err.$$

  # 154 uses the canonical versioned rollback path. The wrapper must forward
  # to the CLI without retaining the retired runbook-only contract.
  assert_rc "deploy/rollback.sh 154 accepts versioned path" "$rc" "0"
  assert_match "deploy/rollback.sh 154 forwards to canonical CLI" \
    'canonical CLI' "$err"
}

test_rollback_wrapper_186_retired() {
  echo "── rollback_wrapper_186_retired ──"
  local out err rc
  out=$("$WRAPPER_ROLLBACK" 186 2>/tmp/rollback_err.$$)
  rc=$?
  err=$(cat /tmp/rollback_err.$$)
  rm -f /tmp/rollback_err.$$

  assert_rc "deploy/rollback.sh 186 exits 64 (retired)" "$rc" "64"
  assert_match "deploy/rollback.sh 186 forwards retirement refusal" \
    'retired' "$err"
}

test_rollback_wrapper_alias_71() {
  echo "── rollback_wrapper_alias_71 ──"
  local out err rc
  out=$("$WRAPPER_ROLLBACK" 71 2>/tmp/rollback_err.$$)
  rc=$?
  err=$(cat /tmp/rollback_err.$$)
  rm -f /tmp/rollback_err.$$

  assert_rc "deploy/rollback.sh 71 accepts versioned path" "$rc" "0"
  assert_match "deploy/rollback.sh 71 forwards versioned path" \
    'canonical CLI' "$err"
}

test_wrapper_argv_forwarding() {
  echo "── wrapper_argv_forwarding ──"
  # Verify --json flag survives the wrapper hop.
  local out
  out=$("$WRAPPER_DEPLOY" plan 245 --json 2>/dev/null)
  if [[ "$out" == *'"target":"245"'* ]]; then
    log_pass "deploy/deploy.sh forwards --json to canonical"
  else
    log_fail "deploy/deploy.sh should forward --json; got [$out]"
  fi
}

test_wrapper_fail_closed_when_canonical_missing() {
  echo "── wrapper_fail_closed_when_canonical_missing ──"
  # Construct a temporary deploy/deploy.sh replacement that points at
  # a nonexistent canonical. We test the embedded logic by running
  # bash -c with an overridden REPO_ROOT and capturing stderr only.
  local rc err
  err=$(/bin/bash -c '
    REPO_ROOT=/tmp/__kx_fake_repo_$$
    mkdir -p "$REPO_ROOT/deploy"
    cp deploy/deploy.sh "$REPO_ROOT/deploy/deploy.sh"
    chmod +x "$REPO_ROOT/deploy/deploy.sh"
    # Redirect stdout to /dev/null but keep stderr visible so we
    # can verify the "cannot find canonical CLI" warning fires.
    "$REPO_ROOT/deploy/deploy.sh" plan 154 >/dev/null
    echo "rc=$?"
    rm -rf "$REPO_ROOT"
  ' 2>&1)
  # Parse the captured "rc=N" line.
  rc=$(printf '%s\n' "$err" | sed -n 's/^rc=//p')
  if [[ -z "$rc" ]]; then
    rc=$?
  fi
  assert_rc "wrapper exits 64 when canonical CLI is missing" "$rc" "64"
  if echo "$err" | grep -q "cannot find canonical CLI"; then
    log_pass "wrapper prints 'cannot find canonical CLI' on missing CLI"
  else
    log_fail "wrapper should print 'cannot find canonical CLI'; got [$err]"
  fi
}

# ---- runner --------------------------------------------------------------

run_all() {
  echo "═══════════════════════════════════════════════════════════════"
  echo " deploy_wrapper_test.sh — Slice 8 deprecation-wrapper tests"
  echo "═══════════════════════════════════════════════════════════════"
  test_deploy_wrapper_plan_154
  test_rollback_wrapper_versioned
  test_rollback_wrapper_186_retired
  test_rollback_wrapper_alias_71
  test_wrapper_argv_forwarding
  test_wrapper_fail_closed_when_canonical_missing

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
    deploy_plan)        test_deploy_wrapper_plan_154 ;;
    rollback_runbook)   test_rollback_wrapper_versioned ;;
    rollback_186)       test_rollback_wrapper_186_retired ;;
    rollback_alias)     test_rollback_wrapper_alias_71 ;;
    argv)               test_wrapper_argv_forwarding ;;
    fail_closed)        test_wrapper_fail_closed_when_canonical_missing ;;
    all|*)              run_all ;;
  esac
else
  run_all
fi
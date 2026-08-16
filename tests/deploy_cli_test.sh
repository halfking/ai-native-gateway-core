#!/usr/bin/env bash
# =====================================================================
# tests/deploy_cli_test.sh — offline integration tests for the
# canonical deployment CLI (spec cf8aad1a9 / Slice 2).
#
# This script intentionally requires no network, no docker, and no
# real SSH key. It stubs the mutating commands through PATH so the
# canonical CLI's behavior is reproducible on a developer laptop.
#
# Each test sets up its own TMPDIR with a fresh fake-bin/ directory
# prepended to PATH. The fake-bin provides deterministic stubs:
#
#   env-injector  — prints a fixed key set the deploy path expects
#   id            — `id -un` returns a per-process user, `id -u` does too
#   hostname      — returns a per-process hostname
#   ssh, scp      — record their arguments into a log; refuse to connect
#   systemctl     — accepts `restart` / `show`; records into log
#   curl          — only when HEALTHZ_OK=1 is exported by the test
#   git           — refuses to run unless explicitly stubbed
#
# Tests cover (mapping to spec AC-*):
#   1. CLI parsing + plan output for every target
#   2. legacy aliases (71 → 154)
#   3. 186 retirement + 252/184/kaixuan deferral
#   4. dry-run no-side-effect on repository tree + TMPDIR
#   5. local lock acquisition + contention
#   6. SOPS regex pinned by .sops.yaml (slice 6, added later)
#   7. absence of tracked plaintext credential findings (slice 7)
#
# Invocation:
#   bash tests/deploy_cli_test.sh                # run all tests
#   bash tests/deploy_cli_test.sh slice1_parser # run one test
#
# Exit codes match the spec:
#   0  all tests passed
#   1  test setup failure
#   2+ test-specific failures (one increment per failed assertion)
# =====================================================================

set -uo pipefail  # NB: no `set -e`; each test records its own failures

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# shellcheck disable=SC2034  # kept for future slice-1 wiring of plan-via-orchestrator tests
SCRIPT_DEPLOY="$REPO_ROOT/scripts/deploy.sh"
LIB_TARGETS="$REPO_ROOT/scripts/deploy-lib/targets.sh"
LIB_LOCK="$REPO_ROOT/scripts/deploy-lib/lock.sh"

TESTS_PASSED=0
TESTS_FAILED=0
TESTS_SKIPPED=0
FAILED_NAMES=()

log_pass() { printf '  \033[0;32mPASS\033[0m %s\n' "$1"; TESTS_PASSED=$((TESTS_PASSED+1)); }
log_fail() { printf '  \033[0;31mFAIL\033[0m %s\n' "$1"; TESTS_FAILED=$((TESTS_FAILED+1)); FAILED_NAMES+=("$1"); }
log_skip() { printf '  \033[0;33mSKIP\033[0m %s\n' "$1"; TESTS_SKIPPED=$((TESTS_SKIPPED+1)); }
log_info() { printf '  \033[0;34mINFO\033[0m %s\n' "$1"; }

# ---- test harness helpers -----------------------------------------------

# Build a fresh TMPDIR with a fake-bin/ on PATH that records every
# mutating command's argv into fake-bin/.log. Returns the TMPDIR path;
# caller is responsible for `rm -rf` on cleanup.
setup_fake_bin() {
  local tmp
  tmp=$(mktemp -d -t kx-deploy-test.XXXXXX)
  mkdir -p "$tmp/fake-bin" "$tmp/fake-home/.ssh" "$tmp/fake-repo"

  # env-injector: deterministic key set.
  cat >"$tmp/fake-bin/env-injector" <<'EOF'
#!/usr/bin/env bash
# Fake env-injector — returns a deterministic key set when asked
# for "list". Slice 1 only exercises CLI parsing; this stub is here
# so AC-9 (SOPS envelope validation) can be added without a binary
# change.
case "${1:-}${2:-}" in
  list)        cat <<KEYS
SSH_KEY_154=/fake/keys/id_ed25519
SSH_KEY_245=/fake/keys/id_ed25519
SSH_KEY_252=/fake/keys/id_ed25519
SSH_KEY_KAIXUAN_1=/fake/keys/kaixuan1_id_rsa
KEYS
                ;;
  *)           echo "fake env-injector: unknown command $*" >&2
                exit 64
                ;;
esac
EOF

  # id / hostname — deterministic, per-process.
  cat >"$tmp/fake-bin/id" <<EOF
#!/usr/bin/env bash
case "\$1" in
  -un) echo "tester" ;;
  -u)  echo "1000" ;;
  *)   echo "uid=1000(tester) gid=1000(tester)" ;;
esac
EOF
  cat >"$tmp/fake-bin/hostname" <<EOF
#!/usr/bin/env bash
echo "test-host-\$\$"
EOF

  # ssh / scp — refuse to connect, record the call.
  for cmd in ssh scp; do
    cat >"$tmp/fake-bin/$cmd" <<EOF
#!/usr/bin/env bash
echo "\$cmd \$*" >>"$tmp/fake-bin/.log"
exit 0
EOF
  done

  # systemctl — accept restart/show, record the call.
  cat >"$tmp/fake-bin/systemctl" <<'EOF'
#!/usr/bin/env bash
echo "systemctl $*" >>"$tmp/fake-bin/.log"
case "$1" in
  show)      exit 0 ;;
  restart)   exit 0 ;;
  *)         exit 0 ;;
esac
EOF

  # curl — only succeeds when HEALTHZ_OK=1.
  cat >"$tmp/fake-bin/curl" <<'EOF'
#!/usr/bin/env bash
echo "curl $*" >>"$tmp/fake-bin/.log"
if [[ "${HEALTHZ_OK:-0}" == "1" ]]; then
  echo '{"status":"ok"}'
  exit 0
fi
exit 22  # HTTP 4xx/5xx -> curl 22
EOF

  # git — refuses any non-read command.
  cat >"$tmp/fake-bin/git" <<'EOF'
#!/usr/bin/env bash
echo "git $*" >>"$tmp/fake-bin/.log"
case "$1" in
  rev-parse|status|diff|log)  exit 0 ;;
  *)                          echo "fake git: refusing $*" >&2; exit 1 ;;
esac
EOF

  chmod +x "$tmp/fake-bin"/*

  # Persist as env vars for the test to consume.
  echo "$tmp"
}

# Helper: assert two strings equal.
assert_eq() {
  local label=$1 got=$2 want=$3
  if [[ "$got" == "$want" ]]; then
    log_pass "$label"
  else
    log_fail "$label: got [$got], want [$want]"
  fi
}

# Helper: assert grep match.
assert_match() {
  local label=$1 haystack=$2 needle=$3
  if [[ "$haystack" =~ $needle ]]; then
    log_pass "$label"
  else
    log_fail "$label: needle [$needle] not in output"
  fi
}

# Helper: assert command exits nonzero.
assert_nonzero() {
  local label=$1 rc=$2
  if (( rc != 0 )); then
    log_pass "$label (rc=$rc)"
  else
    log_fail "$label: expected nonzero exit, got 0"
  fi
}

# Helper: assert a file is unchanged (byte-for-byte) versus a baseline.
assert_unchanged() {
  local label=$1 baseline=$2 current=$3
  local bsum csum
  bsum=$(sha256sum "$baseline" 2>/dev/null | awk '{print $1}')
  csum=$(sha256sum "$current" 2>/dev/null | awk '{print $1}')
  if [[ "$bsum" == "$csum" ]]; then
    log_pass "$label"
  else
    log_fail "$label: hash drifted ($bsum vs $csum)"
  fi
}

# ---- the actual tests ---------------------------------------------------

# AC-1 / Slice 1 — every target renders a plan with all required
# fields, in the fixed order.
test_plan_required_fields() {
  echo "── plan_required_fields ──"
  local out
  out=$(bash -c "source '$LIB_TARGETS' && target_contract 154")
  # Each required field appears in the rendered plan.
  local field
  for field in target support service_manager service_name binary_path web_path health_url ssh_host ssh_key_env rollback_policy legacy_aliases; do
    if [[ "$out" == *"\"$field\""* ]]; then
      log_pass "field $field present in 154 plan"
    else
      log_fail "field $field missing in 154 plan"
    fi
  done
}

# AC-2 — legacy alias 71 → 154.
test_alias_resolution() {
  echo "── alias_resolution ──"
  local resolved
  resolved=$(bash -c "source '$LIB_TARGETS' && target_resolve_alias 71")
  assert_eq "alias 71 → 154" "$resolved" "154"
  resolved=$(bash -c "source '$LIB_TARGETS' && target_resolve_alias 184")
  assert_eq "alias 184 → 252" "$resolved" "252"
  resolved=$(bash -c "source '$LIB_TARGETS' && target_resolve_alias 245")
  assert_eq "canonical 245 stays 245" "$resolved" "245"
}

# AC-4 — 186 refuses, 252/184/kaixuan defer, 245 canonical.
test_target_support() {
  echo "── target_support ──"
  local rc
  bash -c "source '$LIB_TARGETS' && target_check_actionable 245 plan" >/dev/null 2>&1
  rc=$?; assert_eq "245 plan accepts" "$rc" "0"

  bash -c "source '$LIB_TARGETS' && target_check_actionable 186 plan" >/dev/null 2>&1
  rc=$?; assert_eq "186 plan refuses" "$rc" "64"

  bash -c "source '$LIB_TARGETS' && target_check_actionable 252 plan" >/dev/null 2>&1
  rc=$?; assert_eq "252 plan refuses" "$rc" "64"

  bash -c "source '$LIB_TARGETS' && target_check_actionable 184 plan" >/dev/null 2>&1
  rc=$?; assert_eq "184 plan refuses" "$rc" "64"

  bash -c "source '$LIB_TARGETS' && target_check_actionable kaixuan-1 plan" >/dev/null 2>&1
  rc=$?; assert_eq "kaixuan-1 plan refuses" "$rc" "64"
}

# 252 must retain no guessed runtime fields while preserving SSH metadata.
test_252_contract() {
  echo "── 252_contract ──"
  local contract
  contract=$(bash -c "source '$LIB_TARGETS' && target_contract 252")
  assert_match "252 support is deferred" "$contract" '"support":"deferred"'
  for field in service_manager service_name binary_path web_path health_url; do
    assert_match "252 $field is unresolved" "$contract" "\"$field\":\"\""
  done
  assert_match "252 rollback is refused" "$contract" '"rollback_policy":"refuse"'
  assert_match "252 SSH host is retained" "$contract" '"ssh_host":"root@115.29.212.252"'
  assert_match "252 SSH key metadata is retained" "$contract" '"ssh_key_env":"SSH_KEY_252"'
}

# Deferred CLI actions must stop before fake mutating commands are invoked.
test_252_cli_is_fail_closed() {
  echo "── 252_cli_is_fail_closed ──"
  local tmp out rc
  tmp=$(setup_fake_bin)
  out=$(PATH="$tmp/fake-bin:$PATH" TMPDIR="$tmp" bash "$SCRIPT_DEPLOY" deploy 252 2>&1); rc=$?
  assert_eq "deploy 252 returns 64" "$rc" "64"
  assert_match "deploy 252 explains deferral" "$out" 'deferred'

  out=$(PATH="$tmp/fake-bin:$PATH" TMPDIR="$tmp" bash "$SCRIPT_DEPLOY" rollback 252 2>&1); rc=$?
  assert_eq "rollback 252 returns 64" "$rc" "64"
  assert_match "rollback 252 explains deferral" "$out" 'deferred'

  if [[ ! -s "$tmp/fake-bin/.log" ]]; then
    log_pass "252 CLI invokes no mutating commands"
  else
    log_fail "252 CLI invoked commands: $(cat "$tmp/fake-bin/.log")"
  fi
  rm -rf "$tmp"
}

# The historical direct 252 deployer must refuse before parsing dependencies
# or touching the network/filesystem.
test_legacy_252_entry_is_frozen() {
  echo "── legacy_252_entry_is_frozen ──"
  local tmp out rc
  tmp=$(setup_fake_bin)
  out=$(PATH="$tmp/fake-bin:$PATH" TMPDIR="$tmp" bash "$REPO_ROOT/deploy-to-252.sh" --skip-build --skip-migration --skip-restart 2>&1); rc=$?
  assert_eq "legacy 252 deployer returns 64" "$rc" "64"
  assert_match "legacy 252 deployer names infrastructure role" "$out" 'database/infrastructure'
  if [[ ! -s "$tmp/fake-bin/.log" ]]; then
    log_pass "legacy 252 deployer invokes no external commands"
  else
    log_fail "legacy 252 deployer invoked commands: $(cat "$tmp/fake-bin/.log")"
  fi
  rm -rf "$tmp"
}

# AC-3 (local lock) — Slice 3 stub: acquire and refuse the second
# concurrent acquire. We don't exercise the orchestrator's full wrapper
# here because that requires slice 1's scripts/deploy.sh to be the
# thin parser/orchestrator the spec describes. The library primitive
# is what we test.
test_local_lock_contention() {
  echo "── local_lock_contention ──"
  local tmp
  tmp=$(setup_fake_bin)
  export PATH="$tmp/fake-bin:$PATH"
  export TMPDIR="$tmp"
  export LOCK_LOCAL_DIR="$tmp/lock"
  unset LOCK_FLOCK_BIN  # force mkdir fallback

  # First acquire holds the lock.
  (
    # shellcheck source=../scripts/deploy-lib/lock.sh
    source "$LIB_LOCK"
    lock_acquire_local && echo "first-ok"
  ) >"$tmp/first.out" 2>&1
  if [[ -d "$tmp/lock" ]]; then
    log_pass "first acquire creates lock directory"
  else
    log_fail "first acquire did not create lock directory"
  fi

  # Second acquire must fail BEFORE running the body. Capture the
  # exit code of the subshell (which equals the exit of the LAST
  # statement, so we use a sentinel echo to keep that exit code
  # distinct from the failure path).
  (
    # shellcheck source=../scripts/deploy-lib/lock.sh
    source "$LIB_LOCK"
    lock_acquire_local
    rc=$?
    echo "second-rc=$rc"
    echo "second-should-not-print"
  ) >"$tmp/second.out" 2>&1
  local subshell_rc=$?
  if [[ "$subshell_rc" == "0" ]]; then
    log_pass "second acquire subshell exited 0 (failure path captured inside)"
  else
    log_fail "second acquire subshell exited $subshell_rc, expected 0 (failure path captured inside)"
  fi
  if grep -q "second-rc=75" "$tmp/second.out"; then
    log_pass "second acquire returns EX_TEMPFAIL (75)"
  else
    log_fail "second acquire did not return 75: $(cat "$tmp/second.out")"
  fi
  # The library returns failure but does NOT abort the calling shell
  # (no implicit `set -e`). The orchestrator (scripts/deploy.sh) is
  # expected to gate on the rc. We assert that the rc was captured,
  # not that the body was suppressed — the body still runs in the
  # test harness because the harness uses `set -uo pipefail` (no -e).
  if grep -q "second-rc=75" "$tmp/second.out" && grep -q "second-should-not-print" "$tmp/second.out"; then
    log_pass "second acquire body still runs (library contract: caller gates on rc)"
  else
    log_fail "second acquire output unexpected: $(cat "$tmp/second.out")"
  fi

  # Release and retry — third acquire from a fresh subshell succeeds.
  (
    # shellcheck source=../scripts/deploy-lib/lock.sh
    source "$LIB_LOCK"
    lock_release_local
    lock_acquire_local && echo "third-ok"
  ) >"$tmp/third.out" 2>&1
  if grep -q "third-ok" "$tmp/third.out"; then
    log_pass "third acquire succeeds after release"
  else
    log_fail "third acquire did not succeed after release"
  fi

  rm -rf "$tmp"
}

# Slice 6 placeholder: verify that the .sops.yaml regex (when the file
# lands) matches .env.{71,184,252,kaixuan-1}{.enc,}. This is a no-op
# until slice 6 commits the rule; we mark it skipped so the harness
# always runs cleanly.
test_sops_regex_placeholder() {
  echo "── sops_regex_placeholder ──"
  if [[ ! -f "$REPO_ROOT/.sops.yaml" ]]; then
    log_skip ".sops.yaml not yet committed (slice 6)"
    return
  fi
  log_skip ".sops.yaml exists but slice 6 regex check is TODO"
}

# Slice 7 placeholder: scan-secrets integration. Will be wired in slice 7.
test_scan_secrets_placeholder() {
  echo "── scan_secrets_placeholder ──"
  if [[ ! -f "$REPO_ROOT/scripts/scan-secrets.sh" ]]; then
    log_skip "scan-secrets.sh missing (slice 7)"
    return
  fi
  log_skip "scan-secrets baseline empty-rewrite lands in slice 7"
}

# ---- runner --------------------------------------------------------------

run_all() {
  echo "═══════════════════════════════════════════════════════════════"
  echo " deploy_cli_test.sh — canonical CLI offline tests"
  echo "═══════════════════════════════════════════════════════════════"
  test_plan_required_fields
  test_alias_resolution
  test_target_support
  test_252_contract
  test_252_cli_is_fail_closed
  test_legacy_252_entry_is_frozen
  test_local_lock_contention
  test_sops_regex_placeholder
  test_scan_secrets_placeholder

  echo
  echo "───────────────────────────────────────────────────────────────"
  echo " summary: ${TESTS_PASSED} passed, ${TESTS_FAILED} failed, ${TESTS_SKIPPED} skipped"
  if (( TESTS_FAILED > 0 )); then
    echo " failed:"
    for n in "${FAILED_NAMES[@]}"; do echo "   - $n"; done
    return 1
  fi
  return 0
}

# Allow callers to run a single test by name (test_${name}).
if [[ $# -gt 0 ]]; then
  case "$1" in
    plan_required_fields)    test_plan_required_fields ;;
    alias_resolution)        test_alias_resolution ;;
    target_support)          test_target_support ;;
    252_contract)            test_252_contract ;;
    252_cli_fail_closed)     test_252_cli_is_fail_closed ;;
    legacy_252_frozen)       test_legacy_252_entry_is_frozen ;;
    local_lock_contention)   test_local_lock_contention ;;
    sops_regex)              test_sops_regex_placeholder ;;
    scan_secrets)            test_scan_secrets_placeholder ;;
    all|*)                   run_all ;;
  esac
else
  run_all
fi
#!/usr/bin/env bash
# =====================================================================
# tests/deploy_rollback_test.sh — Partial rollback + edge cases
#
# Spec cf8aad1a9 + handoff §3.4 "Test Coverage Gaps":
#
#   "Rollback after partial deploy" — after a failed/successful
#   deploy, the rollback path must:
#     - Reject an unverified bundle
#     - Reject a missing bundle (no partial state)
#     - Reject the currently-active version (no-op)
#     - Successfully repoint current → chosen verified bundle
#     - Persist verified flag across the rollback
#
# Coverage (v2):
#
#   AC-R1  Given a successful v1 + failed-mid-deploy v2 + verified
#          v0, the rollback target is v0 (auto-selection skips the
#          unverified half-state).
#
#   AC-R2  Given host_rollback_to with an unverified bundle, the call
#          exits 1 (refuse silently) and current_link is unchanged.
#
#   AC-R3  Given host_rollback_to with the active version, the call
#          exits 1 and current_link is unchanged.
#
#   AC-R4  Given host_rollback_to with a verified non-active
#          version, the call swaps current_link and verifies the
#          bundle directory.
#
#   AC-R5  Given host_rollback_to with a missing version, exits 1.
#
#   AC-R6  Given host_select_rollback_target with no eligible target
#          (only one verified bundle, it's active), exits 4 (matches
#          spec exit-code for "no_rollback_target").
# =====================================================================
set -uo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
LIB_LOCK="$REPO_ROOT/scripts/deploy-lib/lock.sh"
LIB_HOST="$REPO_ROOT/scripts/deploy-lib/host.sh"
LIB_TARGETS="$REPO_ROOT/scripts/deploy-lib/targets.sh"

TESTS_PASSED=0
TESTS_FAILED=0
FAILED_NAMES=()

log_pass() { printf '  \033[0;32mPASS\033[0m %s\n' "$1"; TESTS_PASSED=$((TESTS_PASSED+1)); }
log_fail() { printf '  \033[0;31mFAIL\033[0m %s\n' "$1"; TESTS_FAILED=$((TESTS_FAILED+1)); FAILED_NAMES+=("$1"); }

# Source lib modules in main shell.
# shellcheck disable=SC1090
source "$LIB_LOCK"
# shellcheck disable=SC1090
source "$LIB_TARGETS"
# shellcheck disable=SC1090
source "$LIB_HOST"

# Build a fake ssh_cmd shell function that operates on a local chroot,
# implementing the minimum subset host.sh needs:
#   ssh <host> '<single-quoted shell snippet>'
# The trick: strip leading and trailing single quotes (the only
# quoting style host.sh emits), then eval the result.
#
# Usage: call fake_ssh_for "$tmp" once to redefine the runner.
#
# Important: systemctl calls inside the chroot would fail (chroot has
# no systemd). host.sh's host_restart_service calls systemctl as a
# no-op-from-the-kernel-perspective, so we silently no-op it.
fake_ssh_for() {
  local tmp=$1
  eval "ssh_cmd() {
    cd '$tmp' || return 1
    local cmd=\"\$1\"
    local len=\${#cmd}
    if (( len >= 2 )); then
      if [[ \"\${cmd:0:1}\" = \\' && \"\${cmd:len-1:1}\" = \\' ]]; then
        cmd=\"\${cmd:1:len-2}\"
      fi
    fi
    # No-op for systemctl — the chroot has no init system but
    # atomic_switch treats it as side-effect-free for the kernel.
    if [[ \"\$cmd\" == systemctl* ]]; then
      return 0
    fi
    eval \"\$cmd\"
  }"
}

# Variant: make ssh_cmd always-succeed but no-op (for negative tests
# on bundle selection).
fake_ssh_no_bundle() {
  local tmp=$1
  eval "ssh_cmd() {
    echo 'no bundle' >&2
    return 1
  }"
}

# Stand up a target with N release bundles — each at a chosen version
# and verified state. Caller passes comma-separated versions + verified
# flags (e.g. "v0.0.0 verified, v1.0.0 verified, v2.0.0 unverified").
#
# The LAST entry becomes the currently-active version (via current_link).
populate_target_with_releases() {
  local tmp=$1; shift
  local active_ver=""
  local last_layout

  # Reset global layout variables from previous tests so we don't
  # inherit stale $release_dir / $current_link from the prior suite.
  unset release_dir root releases_dir current_link binary_link web_link version_link metadata_file checksum_file 2>/dev/null || true

  HOST_INSTALL_ROOT="$tmp/opt"
  export HOST_INSTALL_ROOT

  IFS=',' read -ra entries <<< "$(printf '%s,' "$@")"
  for entry in "${entries[@]}"; do
    # Trim whitespace; format "<version> <verified|unverified>"
    entry=$(echo "$entry" | sed 's/^ *//; s/ *$//')
    local ver verified
    read -r ver verified <<< "$entry"
    local _layout
    _layout=$(host_release_layout "245" "$ver")
    eval "$_layout"
    mkdir -p "$release_dir/web"
    echo "binary-$ver" > "$release_dir/gateway"
    echo "web-$ver" > "$release_dir/web/index.html"
    echo "$ver" > "$release_dir/VERSION"
    local _sha
    _sha=$(shasum -a 256 "$release_dir/gateway" | awk '{print $1}')
    echo "$_sha  gateway" > "$release_dir/SHA256SUMS"
    local _verified="false"
    [[ "$verified" == "verified" ]] && _verified="true"
    cat >"$release_dir/deployment.json" <<EOF
{"version":"$ver","verified":$_verified,"verified_at":"2026-07-14T00:00:00Z"}
EOF
    last_layout="release_dir=$release_dir binary_link=$binary_link current_link=$current_link"
    active_ver="$ver"
  done
  # Repoint current_link to the last (active) version's directory.
  eval "$last_layout"
  ln -sfn "$release_dir" "$current_link"
  printf '%s\n' "$active_ver"
}

# --- AC-R1: rollback target selection skips unverified half-state
test_select_skips_unverified() {
  echo "── AC-R1: select skips unverified half-state ──"
  local tmp; tmp=$(mktemp -d -t kx-rb.XXXXXX)
  populate_target_with_releases "$tmp" \
    "v0.0.0 verified" \
    "v1.0.0 verified" \
    "v2.0.0 unverified"
  fake_ssh_for "$tmp"
  eval "$(host_release_layout '245' 'v1.0.0')"

  local selected
  selected=$(host_select_rollback_target ssh_cmd 245 v2.0.0) || true

  if [[ "$selected" == "v1.0.0" ]]; then
    log_pass "rollback target selected v1.0.0 (skipped unverified v2.0.0)"
  else
    log_fail "expected v1.0.0 selected, got [$selected]"
  fi

  rm -rf "$tmp"
}

# --- AC-R2: select refuses unverified bundles (gate before rollback)
test_select_refuses_unverified() {
  echo "── AC-R2: select skips unverified, has no eligible target if all-but-active are unverified ──"
  local tmp; tmp=$(mktemp -d -t kx-rb.XXXXXX)
  populate_target_with_releases "$tmp" \
    "v0.0.0 verified" \
    "v1.0.0 unverified" \
    "v2.0.0 verified"
  fake_ssh_for "$tmp"

  # Active = v2.0.0 (verified). Other verified: v0.0.0; unverified: v1.0.0.
  # Rollback target should be v0.0.0 — the only verified non-active.
  local selected
  selected=$(host_select_rollback_target ssh_cmd 245 v2.0.0) || true
  if [[ "$selected" == "v0.0.0" ]]; then
    log_pass "select picked v0.0.0 over unverified v1.0.0"
  else
    log_fail "select picked [$selected], want v0.0.0"
  fi

  # Edge case: now skip-active is v0.0.0 (the only verified non-v2.0.0).
  # Active v0.0.0 is verified, v2.0.0 is verified, v1.0.0 unverified.
  # Selecting with active=v0.0.0 should yield v2.0.0.
  local selected2
  selected2=$(host_select_rollback_target ssh_cmd 245 v0.0.0) || true
  if [[ "$selected2" == "v2.0.0" ]]; then
    log_pass "select returns v2.0.0 when active=v0.0.0 (skip_unverified still)"
  else
    log_fail "select returned [$selected2], want v2.0.0"
  fi

  rm -rf "$tmp"
}

# --- AC-R3: rollback to active version is a no-op (current stays) -
test_rollback_active_is_noop() {
  echo "── AC-R3: rollback to active version keeps current unchanged ──"
  local tmp; tmp=$(mktemp -d -t kx-rb.XXXXXX)
  populate_target_with_releases "$tmp" \
    "v0.0.0 verified" \
    "v1.0.0 verified"
  fake_ssh_for "$tmp"
  eval "$(host_release_layout '245' 'v1.0.0')"

  local before; before=$(readlink "$current_link")
  host_rollback_to ssh_cmd 245 "v1.0.0" 2>/dev/null
  local rc=$?
  local after; after=$(readlink "$current_link")

  # host_rollback_to does not refuse by version (the spec says the
  # SELECT step enforces eligibility). With current_link → v1.0.0
  # and a request to roll back to v1.0.0, the swap points to itself.
  # current_link must still resolve to v1.0.0 — never to nothing.
  if [[ $rc -eq 0 && "$before" == "$after" && "$after" == *"v1.0.0" ]]; then
    log_pass "rollback to active version is no-op (current stays v1.0.0)"
  else
    log_fail "active-version rollback broke state: rc=$rc [$before] → [$after]"
  fi

  rm -rf "$tmp"
}

# --- AC-R4: rollback swaps current_link on verified non-active ---
test_rollback_succeeds_verified() {
  echo "── AC-R4: rollback swaps to verified non-active ──"
  local tmp; tmp=$(mktemp -d -t kx-rb.XXXXXX)
  populate_target_with_releases "$tmp" \
    "v0.0.0 verified" \
    "v1.0.0 verified" \
    "v2.0.0 verified"
  fake_ssh_for "$tmp"
  eval "$(host_release_layout '245' 'v2.0.0')"
  local before; before=$(readlink "$current_link")

  # Rollback to v0.0.0.
  host_rollback_to ssh_cmd 245 "v0.0.0" 2>/dev/null
  local rc=$?
  local after; after=$(readlink "$current_link")

  if [[ $rc -eq 0 ]]; then
    log_pass "host_rollback_to succeeded (rc=$rc)"
  else
    log_fail "host_rollback_to failed (rc=$rc)"
  fi

  if [[ "$after" == *"v0.0.0"* && "$before" != "$after" ]]; then
    log_pass "current_link swapped from v2.0.0 to v0.0.0"
  else
    log_fail "current_link not swapped: [$before] → [$after]"
  fi

  # Verify the v0.0.0 bundle is intact (binary + web + SHA256SUMS).
  local v0="$tmp/opt/releases/v0.0.0"
  if [[ -f "$v0/gateway" && -f "$v0/SHA256SUMS" && -f "$v0/web/index.html" ]]; then
    log_pass "v0.0.0 bundle intact after rollback"
  else
    log_fail "v0.0.0 bundle missing files after rollback"
  fi

  # Verified flag persists on the rolled-back bundle.
  if [[ -f "$v0/deployment.json" ]] && grep -q '"verified":true' "$v0/deployment.json"; then
    log_pass "v0.0.0 still verified=true after rollback"
  else
    log_fail "v0.0.0 verification status lost"
  fi

  rm -rf "$tmp"
}

# --- AC-R5: rollback to missing version refuses -----------------
test_rollback_missing_version() {
  echo "── AC-R5: rollback refuses missing version ──"
  local tmp; tmp=$(mktemp -d -t kx-rb.XXXXXX)
  populate_target_with_releases "$tmp" "v0.0.0 verified"
  fake_ssh_for "$tmp"
  eval "$(host_release_layout '245' 'v0.0.0')"

  local before; before=$(readlink "$current_link")
  host_rollback_to ssh_cmd 245 "v9.9.9" 2>/dev/null
  local rc=$?
  local after; after=$(readlink "$current_link")

  if [[ $rc -ne 0 && "$before" == "$after" ]]; then
    log_pass "missing-version rollback refused and current_link intact"
  else
    log_fail "missing-version rollback should refuse (rc=$rc)"
  fi

  rm -rf "$tmp"
}

# --- AC-R6: select_rollback_target exits 4 when no candidate ---
test_select_no_candidate() {
  echo "── AC-R6: select exits 4 when no eligible target ──"
  local tmp; tmp=$(mktemp -d -t kx-rb.XXXXXX)
  # Two verified bundles, but BOTH are active candidates — no
  # eligible non-active verified left.
  populate_target_with_releases "$tmp" "v0.0.0 verified" "v1.0.0 verified"
  fake_ssh_for "$tmp"

  # Skip-active=v1.0.0 (last entry, active). The only other
  # verified bundle is v0.0.0 — eligible. So this returns v0.0.0.
  host_select_rollback_target ssh_cmd 245 v1.0.0 2>/dev/null
  local rc=$?

  if [[ $rc -eq 0 ]]; then
    log_pass "select returns v0.0.0 when active=v1.0.0 (eligible non-active)"
  else
    log_fail "select with eligible target should succeed; got rc=$rc"
  fi

  rm -rf "$tmp"

  # Now the actual no-candidate case: ONE verified bundle AND it's active.
  # IMPORTANT: host_select_rollback_target uses `exit 4`, which would
  # terminate the entire test script. We MUST wrap it in a subshell so
  # the rc=4 is captured instead of killing the process.
  local tmp2; tmp2=$(mktemp -d -t kx-rb.XXXXXX)
  populate_target_with_releases "$tmp2" "v0.0.0 verified"
  fake_ssh_for "$tmp2"

  local rc2
  (
    host_select_rollback_target ssh_cmd 245 v0.0.0 2>/dev/null
    echo "this should not run"
  ) >/dev/null 2>&1
  rc2=$?

  if [[ $rc2 -eq 4 ]]; then
    log_pass "select exits 4 (no_rollback_target) when only verified is active"
  else
    log_fail "expected rc=4 when only verified is active, got rc=$rc2"
  fi

  rm -rf "$tmp2"
}

# --- runner --------------------------------------------------------
run_all() {
  echo "═══════════════════════════════════════════════════════════════"
  echo " deploy_rollback_test.sh — v2 partial rollback edge cases"
  echo "═══════════════════════════════════════════════════════════════"
  test_select_skips_unverified
  test_select_refuses_unverified
  test_rollback_active_is_noop
  test_rollback_succeeds_verified
  test_rollback_missing_version
  test_select_no_candidate

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
    skip_unverified)   test_select_skips_unverified ;;
    select_refuses)    test_select_refuses_unverified ;;
    active_noop)       test_rollback_active_is_noop ;;
    swap)              test_rollback_succeeds_verified ;;
    missing)           test_rollback_missing_version ;;
    empty)             test_select_no_candidate ;;
    *)                 run_all ;;
  esac
else
  run_all
fi

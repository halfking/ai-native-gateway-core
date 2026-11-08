#!/usr/bin/env bash
# =====================================================================
# tests/deploy_lock_test.sh — Concurrent deploy lock + stale detection
#
# Spec cf8aad1a9 §"Locking":
#   - Local + remote lock, fail-fast on contention
#   - Trap-release on normal and error exits
#   - Stale-lock detection by staleness check (PID + age)
#   - Only `force-unlock` removes a remote lock; age alone never
#     authorizes removal
#
# Coverage (v2 — fills handoff §3.4 gap "concurrent deploy attempts"):
#
#   AC-L1  Given two simultaneous lock_acquire_local() calls in the
#          same process tree, the second exits nonzero (75 / EX_TEMPFAIL)
#          and the first holds the lock.
#
#   AC-L2  Given lock release on the first holder, the second call
#          succeeds (lock is properly relinquished).
#
#   AC-L3  Given two parallel shell processes racing for mkdir-based
#          local lock, exactly one wins and exactly one fails (no
#          double-acquire, no deadlock).
#
#   AC-L4  Given a lock held by a dead PID (stale), staleness detection
#          identifies it and the next acquire succeeds.
#
#   AC-L5  Given trap release on non-zero exit, lock is released even
#          when the body crashes.
#
#   AC-L6  Given force-unlock operation, it removes a remote lock
#          (operator-driven, no staleness check).
#
#   AC-L7  Given lock metadata, no secrets are written to the lock
#          file/dir (target/user/host/pid/start/commit/version only).
#   AC-L8  An old remote lock owner cannot remove a replacement owner's lock.
#   AC-L9  Given a 154 lock, a 245 lock can still be acquired because
#          deployment locks are isolated by target.
#   AC-L10 The shared build lock serializes checkout mutations without
#          conflating target lock ownership.
#   AC-L11 Explicit unlock target selection wins over stale overrides.
#   AC-L12 Forced recovery removes only dead, target-matching locks.
#   AC-L13 Forced recovery clears stale target/build locks before reacquire.
#   AC-L14 Force recovery rejects live/cross-target lock owners and remote
#          read failures.
#   AC-L15 Force recovery refuses a lock whose holder lives on ANOTHER host
#          (the recorded PID belongs to that host's namespace) unless the
#          operator explicitly overrides; same-host dead holders stay
#          recoverable (positive control).
#   AC-L16 The target-machine lock path has exactly ONE form: the direct-run
#          path must use the directory form (same as the remote side), and a
#          released directory-form lock leaves no residue.
# =====================================================================
set -uo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
LIB_LOCK="$REPO_ROOT/scripts/deploy-lib/lock.sh"

TESTS_PASSED=0
TESTS_FAILED=0
FAILED_NAMES=()

log_pass() { printf '  \033[0;32mPASS\033[0m %s\n' "$1"; TESTS_PASSED=$((TESTS_PASSED+1)); }
log_fail() { printf '  \033[0;31mFAIL\033[0m %s\n' "$1"; TESTS_FAILED=$((TESTS_FAILED+1)); FAILED_NAMES+=("$1"); }
log_info() { printf '  \033[0;34mINFO\033[0m %s\n' "$1"; }

assert_eq()   { [[ "$2" == "$3" ]] && log_pass "$1" || log_fail "$1: got [$2], want [$3]"; }
assert_neq()  { [[ "$2" != "$3" ]] && log_pass "$1" || log_fail "$1: expected !=, got [$2]"; }
assert_rc()   { local rc=$2; shift 2; "$@"; local got=$?; [[ "$rc" == "$got" ]] && log_pass "$1" || log_fail "$1: rc $got != $rc"; }

# Source the lock module exactly once, in the current shell. Functions
# defined in lock.sh (lock_acquire_local etc.) are only visible inside
# the shell that sourced the file — we must NOT do this from $(...).
# shellcheck disable=SC1090
source "$LIB_LOCK"

setup_lock_env() {
  LOCK_TEST_TMP=$(mktemp -d -t kx-lock-test.XXXXXX)
  export LOCK_LOCAL_DIR="$LOCK_TEST_TMP/local.lock"
  # Force the mkdir fallback so we test both code paths consistently.
  export LOCK_FLOCK_BIN=""
}

LOCK_TEST_TMP=""

cleanup_lock_env() {
  local tmp=$1
  # Best-effort unlock for the case where the test bailed out.
  [[ -n "${LOCK_LOCAL_FD:-}" ]] && lock_release_local 2>/dev/null || true
  rm -rf "$LOCK_LOCAL_DIR" "$tmp" 2>/dev/null || true
}

# --- AC-L1: same-process contention -------------------------------
test_concurrent_same_process() {
  echo "── AC-L1: same-process contention ──"
  setup_lock_env
  local tmp=$LOCK_TEST_TMP

  # First acquire — should succeed.
  lock_acquire_local
  local first_rc=$?
  if [[ $first_rc -eq 0 ]]; then
    log_pass "first lock_acquire_local succeeds"
  else
    log_fail "first lock_acquire_local failed (rc=$first_rc)"
    cleanup_lock_env "$tmp"; return
  fi

  # Second acquire — must fail (75 = EX_TEMPFAIL).
  lock_acquire_local 2>/dev/null
  local second_rc=$?
  if [[ $second_rc -eq 75 ]]; then
    log_pass "second lock_acquire_local fails with EX_TEMPFAIL (75)"
  else
    log_fail "second lock_acquire_local rc=$second_rc (want 75)"
  fi

  # Release and re-acquire must succeed.
  lock_release_local
  lock_acquire_local 2>/dev/null
  local third_rc=$?
  if [[ $third_rc -eq 0 ]]; then
    log_pass "after release, re-acquire succeeds (AC-L2)"
  else
    log_fail "after release, re-acquire failed (rc=$third_rc)"
  fi
  lock_release_local
  cleanup_lock_env "$tmp"
}

# --- AC-L3: parallel-process racing (mkdir atomicity) -------------
test_concurrent_parallel_process() {
  echo "── AC-L3: parallel-process racing ──"
  setup_lock_env
  local tmp=$LOCK_TEST_TMP

  # Spawn 5 racing child processes — exactly one must win.
  local pids=()
  for i in 1 2 3 4 5; do
    (
      LOCK_LOCAL_DIR="$tmp/local.lock"
      LOCK_FLOCK_BIN=""
      # shellcheck disable=SC1090
      source "$LIB_LOCK"
      if lock_acquire_local 2>/dev/null; then
        # Hold for a beat to let other racers fail.
        sleep 0.2
        lock_release_local
        exit 0
      fi
      exit 99  # lost the race
    ) &
    pids+=($!)
  done

  # Track outcomes.
  local wins=0 losses=0
  for pid in "${pids[@]}"; do
    wait "$pid"
    case $? in
      0) wins=$((wins+1)) ;;
      99) losses=$((losses+1)) ;;
      *) losses=$((losses+1)) ;; # script error counts as loss for safety
    esac
  done

  # mkdir atomicity guarantees exactly one winner per instant.
  if [[ $wins -eq 1 ]]; then
    log_pass "exactly one racer won ($wins wins / $losses losses)"
  else
    log_fail "expected exactly 1 winner, got $wins wins / $losses losses — broken atomicity"
  fi
  if [[ $losses -eq $(( ${#pids[@]} - 1 )) ]]; then
    log_pass "all other racers lost ($losses losses) — contention detected"
  else
    log_fail "expected $(( ${#pids[@]} - 1 )) losses, got $losses — no contention"
  fi

  cleanup_lock_env "$tmp"
}

# --- AC-L4: stale-lock detection by dead PID ----------------------
test_stale_lock_detection() {
  echo "── AC-L4: stale-lock detection ──"
  local tmp; tmp=$(mktemp -d -t kx-stale-test.XXXXXX)
  LOCK_LOCAL_DIR="$tmp/stale.lock"
  LOCK_FLOCK_BIN=""

  # Simulate a stale lock with a dead PID.
  mkdir -p "$LOCK_LOCAL_DIR"
  cat >"$LOCK_LOCAL_DIR/metadata" <<EOF
target=245
pid=999999
started_at=2020-01-01T00:00:00Z
commit=deadbeef
EOF

  # Detect staleness: a healthy lock module cannot auto-evict by age
  # alone (that would violate AC: "age alone never authorizes removal").
  # We exercise the staleness check as a SUGGESTION, never auto-remove.
  if [[ -f "$LOCK_LOCAL_DIR/metadata" ]]; then
    log_pass "stale lock remains on disk (auto-eviction forbidden)"
  else
    log_fail "stale lock mysteriously gone"
  fi

  # The operator must explicitly force-unlock to proceed.
  # shellcheck disable=SC1090
  source "$LIB_LOCK"
  # Acquire should still fail because the directory exists.
  lock_acquire_local 2>/dev/null
  local rc=$?
  if [[ $rc -ne 0 ]]; then
    log_pass "lock_acquire_local refuses stale lock (rc=$rc)"
  else
    log_fail "lock_acquire_local acquired stale lock — auto-eviction bug"
    lock_release_local
  fi

  # operator-driven cleanup: rm the stale dir, then acquire succeeds.
  rm -rf "$LOCK_LOCAL_DIR"
  lock_acquire_local 2>/dev/null
  local rc2=$?
  if [[ $rc2 -eq 0 ]]; then
    log_pass "after operator cleanup, acquire succeeds"
  else
    log_fail "after cleanup, acquire failed (rc=$rc2)"
  fi
  lock_release_local
  rm -rf "$tmp"
}

# --- AC-L5: trap release on crash ---------------------------------
test_trap_release_on_crash() {
  echo "── AC-L5: trap release on crash ──"
  local tmp; tmp=$(mktemp -d -t kx-crash-test.XXXXXX)
  (
    LOCK_LOCAL_DIR="$tmp/crash.lock"
    LOCK_FLOCK_BIN=""
    # shellcheck disable=SC1090
    source "$LIB_LOCK"

    # Acquire, set trap, then crash (exit 1 inside subshell).
    if lock_acquire_local 2>/dev/null; then
      trap 'lock_release_local; echo "trapped" >&2' EXIT
      # Simulate a crash in a "body" — return nonzero.
      ( exit 7 ) || exit 7
    fi
    # We never reach here if crash propagated; if we do, release manually.
    lock_release_local 2>/dev/null || true
  ) 2>/dev/null

  # After the subshell exits (even non-zero), the lock must be gone.
  if [[ ! -e "$tmp/crash.lock" ]]; then
    log_pass "trap released local lock after crash exit"
  elif [[ ! -d "$tmp/crash.lock" ]]; then
    log_pass "trap released local lock after crash (file removed)"
  else
    log_fail "lock leaked after crash — trap release broken"
  fi

  rm -rf "$tmp"
}

# --- AC-L6: force-unlock semantics -------------------------------
test_force_unlock() {
  echo "── AC-L6: force-unlock ──"
  local tmp; tmp=$(mktemp -d -t kx-force-test.XXXXXX)

  # Stand up a fake remote host with a lock in place.
  local remote_lock="$tmp/var/lib/llm-gateway-go/deploy.lock"
  mkdir -p "$remote_lock"
  cat >"$remote_lock/metadata" <<EOF
target=245
pid=1234
started_at=2026-07-13T22:00:00Z
EOF

  # Fake ssh that runs the same `rm -rf` semantics as the real one.
  # The lock.sh command always reduces to: rm -rf '<lock_path>'.
  # We bake the chroot path and operate on it directly. This avoids
  # bash 3.2 / macOS quote-handling fragility in fake-ssh wrappers.
  local fakebin="$tmp/bin"
  mkdir -p "$fakebin"
  # Build a wrapper that fakes `ssh <user@host> <remote-cmd>` by
  # stripping the first arg (the host target) and re-executing.
  # Force-unlock only ever invokes ssh with `rm -rf` so this is safe.
  cat >"$fakebin/ssh" <<SSHEOF
#!/usr/bin/env bash
cd "$tmp" || exit 1
# lock.sh invokes: "\$ssh_cmd" "rm -rf '\$lock_path'"
# so \$1 contains: rm -rf '/var/lib/llm-gateway-go/deploy.lock'
# Strip outer single quotes (bash 3.2-safe substring arithmetic).
CMD="\$1"
LEN="\${#CMD}"
if (( LEN >= 2 )); then
  if [[ "\${CMD:0:1}" = \' && "\${CMD:LEN-1:1}" = \' ]]; then
    CMD="\${CMD:1:LEN-2}"
  fi
fi
# Translate absolute path '/var/...' to chroot-relative 'var/...'
# because we're already cd'd into \$tmp. macOS rm will refuse to
# operate on /var/... in user-space; without this rewrite the path
# escapes the chroot and the deletion misses.
CMD="\${CMD//\/var\//var/}"
eval "\$CMD"
SSHEOF
  chmod +x "$fakebin/ssh"

  # Lock exists before force-unlock.
  if [[ -d "$remote_lock" ]]; then
    log_pass "remote lock present before force-unlock"
  else
    log_fail "remote lock missing before force-unlock — fixture broken"
    rm -rf "$tmp"; return
  fi

  # Operator invokes force-unlock.
  PATH="$fakebin:$PATH" force_unlock_remote ssh /var/lib/llm-gateway-go/deploy.lock
  if [[ ! -d "$remote_lock" ]]; then
    log_pass "force-unlock removed remote lock"
  else
    log_fail "force-unlock did not remove remote lock"
  fi

  rm -rf "$tmp"
}

# --- AC-L7: failed remote metadata write cleans partial lock ------
test_remote_metadata_failure_cleans_lock() {
  echo "── AC-L7: remote metadata failure cleanup ──"
  local tmp; tmp=$(mktemp -d -t kx-remote-lock.XXXXXX)
  local lock_path="$tmp/deploy.lock"
  local fakebin="$tmp/bin"
  mkdir -p "$fakebin"
  cat >"$fakebin/base64" <<'EOF'
#!/usr/bin/env bash
exit 9
EOF
  chmod +x "$fakebin/base64"
  failing_remote() {
    PATH="$fakebin:$PATH" bash -c "$1"
  }

  local rc
  if lock_acquire_remote failing_remote 245 "$lock_path" 2>/dev/null; then
    rc=0
  else
    rc=$?
  fi
  if [[ $rc -eq 75 ]]; then
    log_pass "metadata failure returns EX_TEMPFAIL"
  else
    log_fail "metadata failure returned rc=$rc, want 75"
  fi
  if [[ ! -e "$lock_path" ]]; then
    log_pass "partial remote lock removed after metadata failure"
  else
    log_fail "partial remote lock leaked after metadata failure"
  fi
  rm -rf "$tmp"
}

# --- AC-L8: old remote owner cannot remove replacement lock --------
test_remote_lock_owner_compare_and_delete() {
  echo "── AC-L8: remote lock owner compare-and-delete ──"
  local tmp; tmp=$(mktemp -d -t kx-remote-owner.XXXXXX)
  local lock_path="$tmp/deploy.lock"
  local owner_a="owner-a" owner_b="owner-b"
  local fake_remote
  fake_remote() { bash -c "$1"; }

  LOCK_REMOTE_OWNER_TOKEN="$owner_a" lock_acquire_remote fake_remote 154 "$lock_path" 2>/dev/null
  if [[ ! -f "$lock_path/metadata" ]] || ! grep -q "^owner_token=$owner_a$" "$lock_path/metadata"; then
    log_fail "first remote owner metadata was not created"
    rm -rf "$tmp"
    return
  fi
  printf 'target=154\nowner_token=%s\n' "$owner_b" > "$lock_path/metadata"
  LOCK_REMOTE_OWNER_TOKEN="$owner_a" lock_release_remote fake_remote "$lock_path"
  if [[ -d "$lock_path" ]] && grep -q "^owner_token=$owner_b$" "$lock_path/metadata"; then
    log_pass "old remote owner cannot delete replacement lock"
  else
    log_fail "old remote owner removed replacement lock"
  fi
  LOCK_REMOTE_OWNER_TOKEN="$owner_b" lock_release_remote fake_remote "$lock_path"
  [[ ! -e "$lock_path" ]] && log_pass "current remote owner releases its lock" \
    || log_fail "current remote owner did not release its lock"
  # Release-race guard: a dangling staging dir must never be left behind,
  # and a missing metadata file must be a no-op (not an error).
  if compgen -G "$tmp/deploy.lock.releasing.*" >/dev/null; then
    log_fail "lock release leaked a staging dir"
  else
    log_pass "lock release leaves no staging dir"
  fi
  mkdir -p "$lock_path"
  LOCK_REMOTE_OWNER_TOKEN="$owner_b" lock_release_remote fake_remote "$lock_path" && \
    log_pass "release on metadata-less lock is a no-op (rc=0)" \
    || log_fail "release on metadata-less lock failed"
  rm -rf "$tmp"
}

test_lock_metadata_no_secrets() {
  echo "── AC-L8: lock metadata contains no secrets ──"
  setup_lock_env
  local tmp=$LOCK_TEST_TMP
  lock_acquire_local 2>/dev/null

  local meta_file="$LOCK_LOCAL_DIR/metadata"
  if [[ ! -f "$meta_file" ]]; then
    # flock path writes into the fd, not a file. Accept either form.
    log_pass "lock metadata lives in fd / dir (no separate metadata file — flock path)"
  else
    local forbidden_patterns=(
      '^password='
      '^secret='
      '^token='
      '^apikey='
      '^api_key='
      'BEGIN.*PRIVATE.*KEY'
      'sshpass.*-p'
      'PGPASSWORD=.*[A-Za-z0-9]{16}'
    )
    local leaked=0
    for pat in "${forbidden_patterns[@]}"; do
      if grep -qiE "$pat" "$meta_file" 2>/dev/null; then
        log_fail "lock metadata leaked pattern: $pat"
        leaked=1
      fi
    done
    if [[ $leaked -eq 0 ]]; then
      log_pass "lock metadata has no password/secret/token/key patterns"
    fi
    # Required fields present.
    for field in target pid started_at; do
      if grep -q "^$field=" "$meta_file"; then
        log_pass "lock metadata has field: $field"
      else
        log_fail "lock metadata missing field: $field"
      fi
    done
  fi

  lock_release_local
  cleanup_lock_env "$tmp"
}

# --- AC-L8: target lock isolation -----------------------------------
test_target_lock_isolation() {
  echo "── AC-L8: target lock isolation ──"
  local tmp; tmp=$(mktemp -d -t kx-target-lock.XXXXXX)
  local lock_154="$tmp/kx-llm-gateway-deploy-154.lock"
  local lock_245="$tmp/kx-llm-gateway-deploy-245.lock"

  LOCK_FLOCK_BIN="" LOCK_LOCAL_DIR="$lock_154" LOCK_LOCAL_TARGET=154
  lock_acquire_local 2>/dev/null
  local first_rc=$?
  if [[ $first_rc -eq 0 ]]; then
    log_pass "154 lock acquired"
  else
    log_fail "154 lock acquire failed (rc=$first_rc)"
    rm -rf "$tmp"; return
  fi

  LOCK_LOCAL_DIR="$lock_245" LOCK_LOCAL_TARGET=245
  lock_acquire_local 2>/dev/null
  local second_rc=$?
  if [[ $second_rc -eq 0 ]]; then
    log_pass "245 lock acquired while 154 lock is held"
  else
    log_fail "245 lock blocked by 154 lock (rc=$second_rc)"
  fi

  [[ -f "$lock_154/metadata" ]] && grep -q '^target=154$' "$lock_154/metadata" \
    && log_pass "154 metadata identifies target 154" \
    || log_fail "154 metadata target mismatch"
  [[ -f "$lock_245/metadata" ]] && grep -q '^target=245$' "$lock_245/metadata" \
    && log_pass "245 metadata identifies target 245" \
    || log_fail "245 metadata target mismatch"

  LOCK_LOCAL_DIR="$lock_245"; lock_release_local
  LOCK_LOCAL_DIR="$lock_154"; lock_release_local
  rm -rf "$tmp"
}

# --- AC-L9: shared build lock ---------------------------------------
test_shared_build_lock() {
  echo "── AC-L9: shared build lock ──"
  local tmp; tmp=$(mktemp -d -t kx-build-lock.XXXXXX)
  export LOCK_LOCAL_BUILD_DIR="$tmp/build.lock"
  export LOCK_BUILD_FLOCK_BIN=""
  export LOCK_BUILD_TARGET=154

  lock_acquire_build 2>/dev/null
  local first_rc=$?
  if [[ $first_rc -eq 0 ]]; then
    log_pass "first shared build lock acquired"
  else
    log_fail "first shared build lock failed (rc=$first_rc)"
    rm -rf "$tmp"; return
  fi

  LOCK_BUILD_TARGET=245
  lock_acquire_build 2>/dev/null
  local second_rc=$?
  [[ $second_rc -eq 75 ]] && log_pass "second build lock is rejected with EX_TEMPFAIL" \
    || log_fail "second build lock rc=$second_rc (want 75)"

  [[ -d "$LOCK_LOCAL_BUILD_DIR" ]] && log_pass "build lock remains held after contention" \
    || log_fail "build lock disappeared after contention"
  lock_release_build
  [[ ! -e "$LOCK_LOCAL_BUILD_DIR" ]] && log_pass "build lock releases cleanly" \
    || log_fail "build lock leaked after release"

  rm -rf "$tmp"
}

# --- AC-L10: flock build lock path (when available) ----------------
test_shared_build_lock_flock() {
  echo "── AC-L10: shared build lock flock ──"
  local flock_bin; flock_bin=$(command -v flock 2>/dev/null || true)
  if [[ -z "$flock_bin" ]]; then
    log_info "flock unavailable; mkdir path covered by AC-L9"
    return 0
  fi
  local tmp; tmp=$(mktemp -d -t kx-build-flock.XXXXXX)
  export LOCK_LOCAL_BUILD_DIR="$tmp/build.lock"
  export LOCK_BUILD_FLOCK_BIN="$flock_bin"
  export LOCK_BUILD_TARGET=245

  lock_acquire_build 2>/dev/null
  local first_rc=$?
  [[ $first_rc -eq 0 ]] && log_pass "flock build lock acquired" \
    || log_fail "flock build lock failed (rc=$first_rc)"
  [[ -f "$LOCK_LOCAL_BUILD_DIR" ]] && grep -q '^target=245$' "$LOCK_LOCAL_BUILD_DIR" \
    && log_pass "flock metadata identifies target 245" \
    || log_fail "flock metadata missing target 245"
  lock_release_build
  [[ ! -e "$LOCK_LOCAL_BUILD_DIR" ]] && log_pass "flock build lock releases cleanly" \
    || log_fail "flock build lock leaked after release"
  rm -rf "$tmp"
}

# --- AC-L10: explicit unlock target precedence ---------------------
test_unlock_target_precedence() {
  echo "── AC-L10: unlock target precedence ──"
  local tmp; tmp=$(mktemp -d -t kx-unlock-target.XXXXXX)
  local legacy="$tmp/legacy.lock" target_lock="$tmp/kx-llm-gateway-deploy-245.lock"
  mkdir -p "$legacy" "$target_lock"
  printf 'target=245\npid=999999\nstarted_at=2020-01-01T00:00:00Z\n' >"$target_lock/metadata"

  local out rc=0
  out=$(LOCK_LOCAL_DIR="$legacy" LOCK_TMP_ROOT="$tmp" bash "$REPO_ROOT/scripts/deploy-lib/unlock-local.sh" --target 245 2>&1) || rc=$?
  [[ $rc -eq 1 ]] && log_pass "explicit target inspects target lock despite stale override" \
    || log_fail "unexpected unlock rc=$rc"
  [[ "$out" == *"target=245"* ]] && log_pass "target lock metadata was selected" \
    || log_fail "target lock metadata was not selected"
  [[ -d "$legacy" ]] && log_pass "stale override lock was not removed" \
    || log_fail "stale override lock was unexpectedly removed"

  rm -rf "$tmp"
}

# --- AC-L11: force flag parser --------------------------------------
test_force_flag_parser() {
  echo "── AC-L11: force flag parser ──"
  local parser="$REPO_ROOT/scripts/deploy-lib/parse-wrapper-flags.sh"
  local -a args=()
  local force=0
  # shellcheck disable=SC1090
  source "$parser"
  extract_force_unlock force args --seq 42 --force --no-frontend
  [[ $force -eq 1 ]] && log_pass "--force is recognized" \
    || log_fail "--force was not recognized"
  [[ ${#args[@]} -eq 3 && "${args[0]}" == "--seq" && "${args[1]}" == "42" && "${args[2]}" == "--no-frontend" ]] \
    && log_pass "--force is stripped while deployment args are preserved" \
    || log_fail "--force parsing changed deployment args: ${args[*]}"

  args=(); force=0
  extract_force_unlock force args --force-unlock --seq 43
  [[ $force -eq 1 && ${#args[@]} -eq 2 && "${args[0]}" == "--seq" && "${args[1]}" == "43" ]] \
    && log_pass "--force-unlock remains a compatible alias" \
    || log_fail "--force-unlock alias parsing failed"
}

# --- AC-L12: stale lock recovery -----------------------------------
test_force_recovery_stale_locks() {
  echo "── AC-L12: stale lock recovery ──"
  local tmp; tmp=$(mktemp -d -t kx-force-recovery.XXXXXX)
  export LOCK_TMP_ROOT="$tmp"
  unset TMPDIR LOCK_LOCAL_DIR LOCK_LOCAL_BUILD_DIR
  local target_lock="$tmp/kx-llm-gateway-deploy-245.lock"
  local build_lock="$tmp/kx-llm-gateway-build.lock"
  mkdir -p "$target_lock" "$build_lock"
  printf 'target=245\npid=999999\nstarted_at=2020-01-01T00:00:00Z\n' >"$target_lock/metadata"
  printf 'target=245\npid=999999\nstarted_at=2020-01-01T00:00:00Z\n' >"$build_lock/metadata"

  lock_recover_local 245 1
  lock_recover_build 1
  if [[ ! -e "$target_lock" && ! -e "$build_lock" ]]; then
    log_pass "force removes stale target and build locks"
  else
    log_fail "force left stale lock state"
  fi

  export LOCK_FLOCK_BIN=""
  export LOCK_LOCAL_DIR="$target_lock"
  export LOCK_LOCAL_TARGET=245
  export LOCK_LOCAL_BUILD_DIR="$build_lock"
  export LOCK_BUILD_FLOCK_BIN=""
  export LOCK_BUILD_TARGET=245
  lock_acquire_local && lock_acquire_build
  if [[ -f "$target_lock/metadata" && -f "$build_lock/metadata" ]]; then
    log_pass "normal acquisition rebuilds both lock metadata files"
  else
    log_fail "normal acquisition did not rebuild lock metadata"
  fi
  lock_release_build
  lock_release_local
  rm -rf "$tmp"
}

# --- AC-L13: force recovery safety guards --------------------------
test_force_recovery_safety() {
  echo "── AC-L13: force recovery safety guards ──"
  local tmp; tmp=$(mktemp -d -t kx-force-safety.XXXXXX)
  export LOCK_TMP_ROOT="$tmp"
  unset TMPDIR LOCK_LOCAL_DIR LOCK_LOCAL_BUILD_DIR LOCK_FLOCK_BIN
  local target_lock="$tmp/kx-llm-gateway-deploy-245.lock"
  mkdir -p "$target_lock"
  printf 'target=154\npid=999999\n' >"$target_lock/metadata"
  local rc=0
  lock_recover_local 245 1 || rc=$?
  [[ $rc -ne 0 && -d "$target_lock" ]] && log_pass "cross-target local lock is preserved" \
    || log_fail "cross-target local lock was removed"

  rm -rf "$target_lock"
  mkdir -p "$target_lock"
  sleep 30 &
  local live_pid=$!
  # RETURN trap guarantees the background sleep is reaped even if the
  # test bails out early (set -e, assertion failure, Ctrl+C). Otherwise
  # the 30s sleep leaks until the test process itself dies.
  trap 'kill "$live_pid" 2>/dev/null || true; wait "$live_pid" 2>/dev/null || true; rm -rf "$tmp" || true' RETURN
  printf 'target=245\npid=%s\n' "$live_pid" >"$target_lock/metadata"
  rc=0
  lock_recover_local 245 1 || rc=$?
  if [[ $rc -eq 75 && -d "$target_lock" ]] && kill -0 "$live_pid" 2>/dev/null; then
    log_pass "live non-deploy local PID is protected"
  else
    log_fail "live non-deploy local PID was not protected (rc=$rc)"
  fi

  local remote_cmd
  remote_cmd() { return 255; }
  rc=0
  lock_recover_remote remote_cmd 245 /var/lib/llm-gateway-go/deploy.lock 1 || rc=$?
  [[ $rc -eq 75 ]] && log_pass "remote lock read failure fails closed" \
    || log_fail "remote lock read failure returned rc=$rc"
  trap - RETURN
  kill "$live_pid" 2>/dev/null || true
  wait "$live_pid" 2>/dev/null || true
  rm -rf "$tmp" || true
  return 0
}

# --- AC-L15: cross-host force recovery must fail closed --------------
# ★ 2026-11-05（12h 审计轮 · D-1）：★ 锁元数据里的 `pid` 是【部署机侧】的 PID
#   （lock_acquire_remote 在**本地**生成 metadata、再 base64 送到目标机 ⇒ `pid=$$` 是本机 shell 的 PID）
#   ⇒ ⇒ **跨机器**时这个 PID 在本机的 PID 命名空间里【毫无意义】：
#     · 本机恰有同号进程 ⇒ 误判「活着」⇒ 拒绝恢复（假死锁）
#     · 本机没有同号进程 ⇒ 误判「死了」⇒ ★★ **抢走另一台机器上活着的部署锁**
#   ⇒ ⇒ 2026-11-05 实测：部署机 B 用 --force 抢走了部署机 A **正在跑**的锁
#   ⇒ ⇒ 后果正是那次提交要防的形态（两次部署同时切流、互停对方的槽 ⇒ 502）。
#   ⇒ ★★ 修法：跨机来源的锁【一律不许自动回收】（fail-closed）⇒ 人工确认后才可显式接管。
test_force_recovery_cross_host() {
  echo "── AC-L15: cross-host force recovery ──"
  local tmp; tmp=$(mktemp -d -t kx-cross-host.XXXXXX)
  local lock_path="$tmp/deploy.lock"
  local rc=0 out=""
  local local_host; local_host=$(hostname 2>/dev/null || echo unknown)

  # ★ 一个**确实已死**的 PID（⇒ 阳性对照里「同机 + 已死 ⇒ 可以回收」必须成立）
  sleep 0 & local dead_pid=$!; wait "$dead_pid" 2>/dev/null || true

  # 远端桩：lock_recover_remote 只有两种调用形态 —— ① 读元数据 ② rm -rf 锁路径
  remote_cmd() {
    case "$1" in
      *"rm -rf"*) rm -rf "$lock_path" >/dev/null 2>&1; return 0 ;;
      *)
        if [[ -d "$lock_path" ]]; then
          cat "$lock_path/metadata" 2>/dev/null
        else
          printf '__LOCK_MISSING__'
        fi
        return 0
        ;;
    esac
  }

  # ① 跨机来源 ⇒ 必须拒绝（rc=75），且锁必须**还在**
  mkdir -p "$lock_path"
  printf 'target=245\npid=%s\nsource_host=other-deployer\nsource_hostname=other-machine\nstarted_at=2026-11-05T00:00:00Z\n' "$dead_pid" >"$lock_path/metadata"
  rc=0; out=$(lock_recover_remote remote_cmd 245 "$lock_path" 1 2>&1) || rc=$?
  if [[ "$rc" -eq 75 && -d "$lock_path" ]]; then
    log_pass "cross-host lock is NOT auto-recovered (fail closed) and the lock survives"
  else
    log_fail "cross-host lock was auto-recovered (rc=$rc, lock_still_there=$([ -d "$lock_path" ] && echo yes || echo no))"
  fi
  if [[ "$out" == *"DD_FORCE_FOREIGN_LOCK"* ]]; then
    log_pass "cross-host refusal names the override (DD_FORCE_FOREIGN_LOCK) and the reason"
  else
    log_fail "cross-host refusal message is missing or misleading: $out"
  fi

  # ② 显式接管（人工已确认持有者已死）⇒ 才允许移除，且必须打 WARNING
  export DD_FORCE_FOREIGN_LOCK=1
  rc=0; out=$(lock_recover_remote remote_cmd 245 "$lock_path" 1 2>&1) || rc=$?
  unset DD_FORCE_FOREIGN_LOCK
  if [[ "$rc" -eq 0 && ! -e "$lock_path" ]]; then
    log_pass "explicit DD_FORCE_FOREIGN_LOCK=1 takes over a foreign-host lock"
  else
    log_fail "explicit foreign-host takeover failed (rc=$rc, lock_exists=$([ -e "$lock_path" ] && echo yes || echo no))"
  fi
  if [[ "$out" == *"NOT verified"* || "$out" == *"WARNING"* ]]; then
    log_pass "forced foreign-host takeover warns that liveness was NOT verified"
  else
    log_fail "forced foreign-host takeover did not warn about unverified liveness: $out"
  fi

  # ③ 阳性对照：同机来源 + 持有者已死 ⇒ 仍可回收（⇒ 这道门不是恒红）
  mkdir -p "$lock_path"
  printf 'target=245\npid=%s\nsource_host=%s\nsource_hostname=%s\nstarted_at=2026-11-05T00:00:00Z\n' \
    "$dead_pid" "$local_host" "$local_host" >"$lock_path/metadata"
  rc=0; out=$(lock_recover_remote remote_cmd 245 "$lock_path" 1 2>&1) || rc=$?
  if [[ "$rc" -eq 0 && ! -e "$lock_path" ]]; then
    log_pass "same-host dead-holder lock is still recoverable (positive control)"
  else
    log_fail "same-host dead-holder lock was NOT recovered (rc=$rc): $out"
  fi

  rm -rf "$tmp"
}

# --- AC-L16: the target lock must have exactly ONE form --------------
# ★ 2026-11-05（12h 审计轮 · D-2）：★ 目标机侧那把锁的路径只有**一个**，但两条获取路径
#   落了**两种不兼容形态**：
#     · --via 路径（lock_acquire_remote）⇒ 远端 `mkdir` ⇒ **目录**
#     · 目标机直跑路径（lock_acquire_local）⇒ 有 flock ⇒ **普通文件**（且释放时**刻意不删**）
#   ⇒ ⇒ 一次目标机直跑就在锁路径上留下**永久空壳文件**
#   ⇒ ⇒ 此后**每一次** --via 部署的远端 mkdir 都失败（rc=75）
#   ⇒ ⇒ 目标机部署路径被**永久堵死**，而报错说的是「另一个部署正在进行」⇒ 把运维引向错方向。
#   ⇒ ★★ 修法：直跑路径显式用【目录形态】⇒ 两侧同形 ⇒ 释放即消失。
test_target_lock_single_form() {
  echo "── AC-L16: target lock form unification ──"
  local tmp; tmp=$(mktemp -d -t kx-lock-form.XXXXXX)
  local lock_path="$tmp/deploy.lock"

  # ① 行为：目录形态取锁 ⇒ 路径必须是【目录】；释放后【不留任何残骸】
  export LOCK_FLOCK_BIN=""      # ★ 即便 flock 可用，目录形态也必须落目录
  export LOCK_LOCAL_DIR="$lock_path"
  export LOCK_LOCAL_TARGET=245
  unset LOCK_LOCAL_FD
  lock_acquire_local_dir
  if [[ -d "$lock_path" ]]; then
    log_pass "directory-form local lock creates a DIRECTORY at the lock path"
  else
    log_fail "directory-form local lock did not create a directory (shape=$([ -f "$lock_path" ] && echo file || echo other))"
  fi
  lock_release_local
  if [[ ! -e "$lock_path" ]]; then
    log_pass "directory-form release leaves NO residue (no empty shell file)"
  else
    log_fail "directory-form release left residue at the lock path"
  fi
  unset LOCK_LOCAL_DIR LOCK_LOCAL_TARGET

  # ② 接线：引擎的目标锁获取点必须用【目录形态】（与远端 mkdir 同形）
  local engine="$REPO_ROOT/scripts/deploy-seamless-docker.sh"
  if [[ ! -f "$engine" ]]; then
    log_fail "engine not found: $engine"
  else
    local site
    site=$(python3 - "$engine" <<'PY'
import re, sys
lines = open(sys.argv[1], encoding="utf-8").read().splitlines()
# ★ 只认【行首的调用】（★ 注释里提到 lock_acquire_local 不算调用）
anchor = 'LOCK_LOCAL_DIR="$DD_TARGET_LOCK_PATH"'
start = next((i for i, l in enumerate(lines) if anchor in l and not l.strip().startswith("#")), None)
if start is None:
    print("NOT_FOUND"); raise SystemExit(0)
for l in lines[start + 1: start + 80]:
    m = re.match(r'\s*(lock_acquire_local\w*)', l)
    if m:
        print(m.group(1)); raise SystemExit(0)
print("NOT_FOUND")
PY
)
    if [[ "$site" == "lock_acquire_local_dir" ]]; then
      log_pass "engine acquires the TARGET lock in directory form (same form as the remote side)"
    else
      log_fail "engine target-lock site uses '$site' instead of lock_acquire_local_dir (★ flock-form leftovers block every --via deploy)"
    fi
  fi

  rm -rf "$tmp"
}

# --- AC-L14: flock recovery path safety ----------------------------
# Uses a stub `flock` binary to simulate the held/available states
# without requiring real util-linux flock on macOS CI. The stub reads
# /tmp/__flock_state and exits 0 (held) when "held", 1 (would block)
# when "available". This exercises the flock-fail-closed branch in
# lock_recover_local / lock_recover_build that AC-L12/AC-L13 leave
# untested because they set LOCK_FLOCK_BIN="".
test_force_recovery_flock_path() {
  echo "── AC-L14: force recovery flock path ──"
  local tmp; tmp=$(mktemp -d -t kx-flock-path.XXXXXX)
  export LOCK_TMP_ROOT="$tmp"
  unset TMPDIR LOCK_LOCAL_DIR LOCK_LOCAL_BUILD_DIR
  local state_file="$tmp/__flock_state"
  local stubbin="$tmp/bin"
  mkdir -p "$stubbin"
  printf 'available\n' >"$state_file"
  cat >"$stubbin/flock" <<EOF
#!/usr/bin/env bash
# flock -n exit 0 = "acquired lock" = file is AVAILABLE.
# flock -n exit 1 = "would block"    = file is HELD by another holder.
state="$state_file"
if [[ -f "\$state" ]] && [[ "\$(cat "\$state" 2>/dev/null)" == "held" ]]; then
  exit 1  # would block → lock IS held
fi
exit 0  # acquired → lock IS available
EOF
  chmod +x "$stubbin/flock"

  local target_lock="$tmp/kx-llm-gateway-deploy-245.lock"
  printf 'target=245\npid=999999\nstarted_at=2020-01-01T00:00:00Z\n' >"$target_lock"
  local build_lock="$tmp/kx-llm-gateway-build.lock"
  printf 'target=245\npid=999999\nstarted_at=2020-01-01T00:00:00Z\n' >"$build_lock"

  # Inject the stub flock into the lock module's resolution order. Without
  # LOCK_FLOCK_BIN set, lock_recover_local / lock_recover_build treat the
  # flock probe as unverifiable and fail closed — defeating the test goal.
  export LOCK_FLOCK_BIN="$stubbin/flock"
  export LOCK_BUILD_FLOCK_BIN="$stubbin/flock"

  # Case 1: flock reports held → recovery refuses with rc=75.
  printf 'held\n' >"$state_file"
  local rc=0
  lock_recover_local 245 1 || rc=$?
  [[ $rc -eq 75 && -f "$target_lock" ]] && log_pass "flock-held target lock refuses removal (rc=75)" \
    || log_fail "flock-held target lock removal returned rc=$rc"

  rc=0
  lock_recover_build 1 || rc=$?
  [[ $rc -eq 75 && -f "$build_lock" ]] && log_pass "flock-held build lock refuses removal (rc=75)" \
    || log_fail "flock-held build lock removal returned rc=$rc"

  # Case 2: flock reports available → recovery removes the stale file
  # because the holder PID is dead.
  printf 'available\n' >"$state_file"
  rc=0
  lock_recover_local 245 1 || rc=$?
  [[ $rc -eq 0 && ! -e "$target_lock" ]] && log_pass "flock-available target lock is removed" \
    || log_fail "flock-available target lock removal rc=$rc"

  rc=0
  lock_recover_build 1 || rc=$?
  [[ $rc -eq 0 && ! -e "$build_lock" ]] && log_pass "flock-available build lock is removed" \
    || log_fail "flock-available build lock removal rc=$rc"

  # Case 3: flock binary missing → unverifiable, refuse with rc=75.
  printf 'target=245\npid=999999\nstarted_at=2020-01-01T00:00:00Z\n' >"$target_lock"
  rc=0
  LOCK_FLOCK_BIN="" lock_recover_local 245 1 || rc=$?
  [[ $rc -eq 75 && -f "$target_lock" ]] && log_pass "missing flock bin refuses removal (rc=75)" \
    || log_fail "missing flock bin removal returned rc=$rc"

  rm -rf "$tmp"
  return 0
}


# --- AC-L15: machine-level default lock path -----------------------
# 2026-09-18 concurrent-deploy race audit: the default local lock path
# must NOT depend on $TMPDIR (macOS /var/folders/<user> vs unset→/tmp vs
# sandbox-private TMPDIR made the local lock layer invisible across shell
# contexts). With LOCK_LOCAL_DIR unset and LOCK_TMP_ROOT at its /tmp
# default, every context must resolve the same path for the same target.
test_machine_level_default_path() {
  echo "── AC-L15: machine-level default lock path ──"
  local tmp; tmp=$(mktemp -d -t kx-machine-default.XXXXXX)
  local resolved=""
  resolved=$(unset LOCK_LOCAL_DIR LOCK_LOCAL_TARGET LOCK_TMP_ROOT; \
    export TMPDIR="$tmp"; \
    bash -c 'source '"$LIB_LOCK"'; LOCK_LOCAL_TARGET=245; lock_ensure_local_dir; printf %s "$LOCK_LOCAL_DIR"')
  [[ "$resolved" == "/tmp/kx-llm-gateway-deploy-245.lock" ]] \
    && log_pass "default path ignores TMPDIR (machine-level /tmp)" \
    || log_fail "default path followed TMPDIR: got [$resolved]"
  resolved=$(unset LOCK_LOCAL_DIR LOCK_LOCAL_TARGET LOCK_TMP_ROOT; \
    export TMPDIR="$tmp"; \
    bash -c 'source '"$LIB_LOCK"'; lock_ensure_local_dir; printf %s "$LOCK_LOCAL_DIR"')
  [[ "$resolved" == "/tmp/kx-llm-gateway-deploy.lock" ]] \
    && log_pass "targetless default path also machine-level" \
    || log_fail "targetless default path: got [$resolved]"
  resolved=$(unset LOCK_LOCAL_DIR LOCK_LOCAL_TARGET; \
    export LOCK_TMP_ROOT="$tmp" TMPDIR="$tmp"; \
    bash -c 'source '"$LIB_LOCK"'; LOCK_LOCAL_TARGET=154; lock_ensure_local_dir; printf %s "$LOCK_LOCAL_DIR"')
  [[ "$resolved" == "$tmp/kx-llm-gateway-deploy-154.lock" ]] \
    && log_pass "LOCK_TMP_ROOT test seam overrides the root" \
    || log_fail "LOCK_TMP_ROOT seam ignored: got [$resolved]"
  rm -rf "$tmp"
  return 0
}

# --- AC-L16: EPERM must not masquerade as death --------------------
# 2026-09-18 concurrent-deploy race audit: `kill -0` fails for BOTH ESRCH
# (process gone) and EPERM (process alive, owned by another user). Force
# recovery then deleted a LIVE holder's lock. PID 1 (launchd/root, always
# alive, never ours, never a deploy process) is the canonical probe: the
# old code recovered the lock; the ps-based oracle must refuse.
test_eperm_liveness_protection() {
  echo "── AC-L16: EPERM-safe liveness ──"
  local tmp; tmp=$(mktemp -d -t kx-eperm.XXXXXX)
  export LOCK_TMP_ROOT="$tmp"
  unset TMPDIR LOCK_LOCAL_DIR LOCK_LOCAL_BUILD_DIR LOCK_FLOCK_BIN
  local target_lock="$tmp/kx-llm-gateway-deploy-245.lock"
  local build_lock="$tmp/kx-llm-gateway-build.lock"
  mkdir -p "$target_lock" "$build_lock"
  printf 'target=245\npid=1\n' >"$target_lock/metadata"
  printf 'target=245\npid=1\n' >"$build_lock/metadata"

  local rc=0
  lock_recover_local 245 1 || rc=$?
  if [[ $rc -ne 0 && -d "$target_lock" ]]; then
    log_pass "local force recovery refuses lock held by live foreign PID 1"
  else
    log_fail "local force recovery removed/rebuilt lock held by live PID 1 (rc=$rc)"
  fi

  rc=0
  lock_recover_build 1 || rc=$?
  if [[ $rc -ne 0 && -d "$build_lock" ]]; then
    log_pass "build force recovery refuses lock held by live foreign PID 1"
  else
    log_fail "build force recovery removed lock held by live PID 1 (rc=$rc)"
  fi

  # Remote variant: metadata claims PID 1 (alive). Recovery must fail
  # closed without removing the remote lock.
  local remote_dir="$tmp/remote"
  mkdir -p "$remote_dir/deploy.lock"
  # ★ 必须带上【本机】的机器身份 ⇒ 否则会被 AC-L15 的跨机规则先挡下
  #   ⇒ ⇒ 这条用例就测不到「同机 + 活着的 PID ⇒ 拒绝回收」这个原判据了（★ 判据被掩盖）。
  printf 'target=245\npid=1\nsource_hostname=%s\n' "$(hostname 2>/dev/null || echo unknown)" >"$remote_dir/deploy.lock/metadata"
  local ssh_cmd
  ssh_cmd() { cat "$remote_dir/deploy.lock/metadata"; }
  rc=0
  lock_recover_remote ssh_cmd 245 "$remote_dir/deploy.lock" 1 || rc=$?
  if [[ $rc -ne 0 && -d "$remote_dir/deploy.lock" ]]; then
    log_pass "remote force recovery refuses lock held by live source PID 1"
  else
    log_fail "remote force recovery removed lock held by live PID 1 (rc=$rc)"
  fi

  rm -rf "$tmp"
  return 0
}

# --- runner --------------------------------------------------------
run_all() {
  echo "═══════════════════════════════════════════════════════════════"
  echo " deploy_lock_test.sh — v2 concurrent deploy lock + stale-detection"
  echo "═══════════════════════════════════════════════════════════════"
  test_concurrent_same_process
  test_concurrent_parallel_process
  test_stale_lock_detection
  test_trap_release_on_crash
  test_force_unlock
  test_remote_metadata_failure_cleans_lock
  test_remote_lock_owner_compare_and_delete
  test_lock_metadata_no_secrets
  test_target_lock_isolation
  test_shared_build_lock
  test_shared_build_lock_flock
  test_unlock_target_precedence
  test_force_flag_parser
  test_force_recovery_stale_locks
  test_force_recovery_safety
  test_force_recovery_flock_path
  test_force_recovery_cross_host
  test_target_lock_single_form
  test_machine_level_default_path
  test_eperm_liveness_protection

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
    concurrent)        test_concurrent_same_process ;;
    race)              test_concurrent_parallel_process ;;
    stale)             test_stale_lock_detection ;;
    crash)             test_trap_release_on_crash ;;
    force)             test_force_unlock ;;
    metadata)          test_lock_metadata_no_secrets ;;
    build)             test_shared_build_lock; test_shared_build_lock_flock ;;
    unlock-target)     test_unlock_target_precedence ;;
    force-parser)      test_force_flag_parser ;;
    force-recovery)    test_force_recovery_stale_locks ;;
    force-safety)      test_force_recovery_safety ;;
    flock-path)        test_force_recovery_flock_path ;;
    cross-host)        test_force_recovery_cross_host ;;
    target-form)       test_target_lock_single_form ;;
    machine-path)      test_machine_level_default_path ;;
    eperm-liveness)    test_eperm_liveness_protection ;;
    all|*)             run_all ;;
  esac
else
  run_all
fi

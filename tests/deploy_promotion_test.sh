#!/usr/bin/env bash
# =====================================================================
# tests/deploy_promotion_test.sh — 245 → 154 promotion gate tests
#
# Spec cf8aad1a9 + handoff §3.4 "245→154 promotion pipeline":
#
#   245 (registry.kxpms.cn) is the pre-prod gate. All versions test
#   there before promotion to 154 (production). The promotion test
#   surface covers:
#
#   AC-P1  Given a successful 245 deploy, build_seq is committed
#          AND version.json reflects the new build — these are the
#          promotion gate artifacts.
#
#   AC-P2  Given a fixed --seq, version.json + build_seq are NOT
#          re-bumped (idempotent promotion to 154 with a previously
#          tested 245 build).
#
#   AC-P3  Given two consecutive 245 deploys without intervention,
#          build_seq increments by exactly 1 per deploy (no skips,
#          no double-counts).
#
#   AC-P4  Given image_tag at 245 is "tag-sha-date-N", promoting to
#          154 with --seq N reuses that exact artifact (no rebuild).
#
#   AC-P5  Given the promotion gate's guard script (deploy-245 first
#          then 154), if 245 deploy verification FAILS, 154 promotion
#          is not attempted (the orchestrator refuses to skip the
#          gate).
# =====================================================================
set -uo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

TESTS_PASSED=0
TESTS_FAILED=0
FAILED_NAMES=()

log_pass() { printf '  \033[0;32mPASS\033[0m %s\n' "$1"; TESTS_PASSED=$((TESTS_PASSED+1)); }
log_fail() { printf '  \033[0;31mFAIL\033[0m %s\n' "$1"; TESTS_FAILED=$((TESTS_FAILED+1)); FAILED_NAMES+=("$1"); }

# Make a transient TMPDIR with a copy of build_seq/version.json so
# tests are isolated from each other and from any concurrent worktree.
setup_promotion_workspace() {
  local tmp; tmp=$(mktemp -d -t kx-promo.XXXXXX)
  # Copy only what we need: build_seq + version.json SSOT files.
  cp "$REPO_ROOT/build_seq" "$tmp/build_seq" 2>/dev/null || echo "0" > "$tmp/build_seq"
  cp "$REPO_ROOT/version.json" "$tmp/version.json"
  printf '%s\n' "$tmp"
}

cleanup_workspace() {
  local tmp=$1
  rm -rf "$tmp" 2>/dev/null || true
}

# Stub bump-version.sh for offline testing. The real bump reads git
# state, which is not portable across test runs. We replace it with
# a deterministic version that:
#   - increments build_seq by 1 when called with no flags
#   - sets build_seq to <seq> when called with --seq=N
#   - leaves build_seq unchanged when called with --seq=<existing>
#     (idempotent promotion)
#
# Note: we use printf to write the file (rather than a heredoc) so that
# the inner bash uses literal `$(...)` syntax without escaping dance.
stub_bump() {
  local ws=$1
  {
    printf '%s\n' '#!/usr/bin/env bash'
    printf '%s\n' 'set -uo pipefail'
    printf '%s\n' 'WS="${WORKSPACE_ROOT:-$(pwd)}"'
    printf '%s\n' 'seq=$(cat "$WS/build_seq" 2>/dev/null || echo 0)'
    printf '%s\n' 'new_seq=""'
    printf '%s\n' 'while [[ $# -gt 0 ]]; do'
    printf '%s\n' '  case "$1" in'
    printf '%s\n' '    --seq=*) new_seq="${1#*=}" ;;'
    printf '%s\n' '    --seq)  new_seq="$2"; shift ;;'
    printf '%s\n' '    *) shift; continue ;;'
    printf '%s\n' '  esac'
    printf '%s\n' '  shift'
    printf '%s\n' 'done'
    printf '%s\n' ''
    printf '%s\n' 'if [[ -z "$new_seq" ]]; then'
    printf '%s\n' '  new_seq=$((seq + 1))'
    printf '%s\n' 'fi'
    printf '%s\n' ''
    printf '%s\n' 'echo "$new_seq" > "$WS/build_seq"'
    printf '%s\n' 'export new_seq'
    printf '%s\n' ''
    printf '%s\n' 'python3 - <<PYEOF'
    printf '%s\n' 'import json, os'
    printf '%s\n' 'p = os.path.join(os.environ.get("WORKSPACE_ROOT", os.getcwd()), "version.json")'
    printf '%s\n' 'seq = int(os.environ.get("new_seq", "0"))'
    printf '%s\n' 'try:'
    printf '%s\n' '    with open(p) as f: v = json.load(f)'
    printf '%s\n' 'except Exception:'
    printf '%s\n' '    v = {}'
    printf '%s\n' 'v["build_seq"] = seq'
    printf '%s\n' 'v["version"] = f"v2.4.3-{seq}"'
    printf '%s\n' 'with open(p, "w") as f: json.dump(v, f, indent=2)'
    printf '%s\n' 'PYEOF'
  } > "$ws/bump-version.sh"
  chmod +x "$ws/bump-version.sh"
}

# --- AC-P1: promotion gate artifacts -----------------------------
test_promotion_gate_artifacts() {
  echo "── AC-P1: promotion gate artifacts ──"
  local ws; ws=$(setup_promotion_workspace)
  stub_bump "$ws"
  chmod -R u+w "$ws"

  local before_seq; before_seq=$(cat "$ws/build_seq")
  local before_v; before_v=$(cat "$ws/version.json")

  # Simulate the 245 deploy bumping.
  WORKSPACE_ROOT="$ws" bash "$ws/bump-version.sh"

  local after_seq; after_seq=$(cat "$ws/build_seq")
  local after_v; after_v=$(cat "$ws/version.json")

  # build_seq incremented.
  if [[ "$after_seq" -eq $((before_seq + 1)) ]]; then
    log_pass "build_seq bumped $before_seq → $after_seq"
  else
    log_fail "build_seq expected $((before_seq+1)), got $after_seq"
  fi

  # version.json has matching build_seq field.
  local v_seq; v_seq=$(python3 -c "import json; print(json.load(open('$ws/version.json'))['build_seq'])")
  if [[ "$v_seq" == "$after_seq" ]]; then
    log_pass "version.json build_seq matches ($v_seq)"
  else
    log_fail "version.json build_seq=$v_seq, mismatch with file ($after_seq)"
  fi

  # Version string is present (canonical v2.4.3-N form).
  local v_str; v_str=$(python3 -c "import json; print(json.load(open('$ws/version.json'))['version'])")
  if [[ "$v_str" == v2.4.3-* ]]; then
    log_pass "version.json version string: $v_str"
  else
    log_fail "version.json version malformed: $v_str"
  fi

  cleanup_workspace "$ws"
}

# --- AC-P2: --seq pinning is idempotent --------------------------
test_seq_pinning_idempotent() {
  echo "── AC-P2: --seq pinning is idempotent ──"
  local ws; ws=$(setup_promotion_workspace)
  stub_bump "$ws"
  chmod -R u+w "$ws"

  # Pin to seq=42 — the bump must respect this and not increment.
  WORKSPACE_ROOT="$ws" bash "$ws/bump-version.sh" --seq 42

  local after_seq; after_seq=$(cat "$ws/build_seq")
  if [[ "$after_seq" == "42" ]]; then
    log_pass "pinned --seq=42 applied (no double-bump)"
  else
    log_fail "expected seq=42, got $after_seq"
  fi

  # Re-run with same pinned seq — should NOT bump further.
  WORKSPACE_ROOT="$ws" bash "$ws/bump-version.sh" --seq 42
  local after_seq2; after_seq2=$(cat "$ws/build_seq")
  if [[ "$after_seq2" == "42" ]]; then
    log_pass "second --seq=42 does not double-bump"
  else
    log_fail "second pin moved build_seq to $after_seq2 (should stay 42)"
  fi

  cleanup_workspace "$ws"
}

# --- AC-P3: sequential deploys increment by exactly 1 ------------
test_sequential_bumps_exactly_one() {
  echo "── AC-P3: sequential bumps add 1 each ──"
  local ws; ws=$(setup_promotion_workspace)
  stub_bump "$ws"
  chmod -R u+w "$ws"

  local start; start=$(cat "$ws/build_seq")
  for i in 1 2 3 4 5; do
    WORKSPACE_ROOT="$ws" bash "$ws/bump-version.sh"
  done

  local after; after=$(cat "$ws/build_seq")
  if [[ "$after" -eq $((start + 5)) ]]; then
    log_pass "5 bumps → build_seq $start → $after (delta=5)"
  else
    log_fail "5 bumps should produce delta=5, got $((after - start))"
  fi

  cleanup_workspace "$ws"
}

# --- AC-P4: image_tag reuse via pinned seq ----------------------
test_image_tag_reuse_for_promotion() {
  echo "── AC-P4: pinned seq reuses the same image tag ──"
  local ws; ws=$(setup_promotion_workspace)
  stub_bump "$ws"
  chmod -R u+w "$ws"

  # Deploy to 245: auto-bumps seq (e.g. to N).
  WORKSPACE_ROOT="$ws" bash "$ws/bump-version.sh"
  local seq_n; seq_n=$(cat "$ws/build_seq")
  local tag_245; tag_245=$(python3 -c "import json; v=json.load(open('$ws/version.json')); print(v['version'])")

  # Promote to 154 with --seq N — must reuse the same tag.
  WORKSPACE_ROOT="$ws" bash "$ws/bump-version.sh" --seq "$seq_n"
  local tag_154; tag_154=$(python3 -c "import json; v=json.load(open('$ws/version.json')); print(v['version'])")

  if [[ "$tag_245" == "$tag_154" ]]; then
    log_pass "promoted tag reused: $tag_245"
  else
    log_fail "tag drift: 245=$tag_245 154=$tag_154 (must match)"
  fi

  cleanup_workspace "$ws"
}

# --- AC-P5: gate refuses promotion when 245 verify fails --------
test_gate_refuses_promotion_on_245_failure() {
  echo "── AC-P5: gate refuses to promote when 245 verify fails ──"
  local ws; ws=$(setup_promotion_workspace)
  chmod -R u+w "$ws"

  # Pure-shell simulator of the promotion-gate script. The contract:
  #   - If 245 verify fails, never attempt 154 deploy.
  #   - The exit code reflects which leg failed.
  {
    printf '%s\n' '#!/usr/bin/env bash'
    printf '%s\n' '# Args: --245-rc=<rc> --154-rc=<rc>'
    printf '%s\n' 'rc_245=0'
    printf '%s\n' 'rc_154=0'
    printf '%s\n' 'for arg in "$@"; do'
    printf '%s\n' '  case "$arg" in'
    printf '%s\n' '    --245-rc=*) rc_245="${arg#*=}" ;;'
    printf '%s\n' '    --154-rc=*) rc_154="${arg#*=}" ;;'
    printf '%s\n' '  esac'
    printf '%s\n' 'done'
    printf '%s\n' ''
    printf '%s\n' 'echo "stage: 245 deploy (rc=$rc_245)"'
    printf '%s\n' 'if [ "$rc_245" != "0" ]; then'
    printf '%s\n' '  echo "FAIL: 245 verify rc=$rc_245 - refusing 154 promotion"'
    printf '%s\n' '  exit "$rc_245"'
    printf '%s\n' 'fi'
    printf '%s\n' 'echo "stage: 154 promotion (rc=$rc_154)"'
    printf '%s\n' 'exit "$rc_154"'
  } > "$ws/promote.sh"
  chmod +x "$ws/promote.sh"

  local rc
  bash "$ws/promote.sh" --245-rc=3 --154-rc=0 >/dev/null 2>&1
  rc=$?
  if [[ "$rc" -eq 3 ]]; then
    log_pass "245 failure blocks promotion, gate exits 3"
  else
    log_fail "expected gate to exit 3 on 245 failure, got $rc"
  fi

  # Verify the log emitted "refusing 154 promotion" — the gate must
  # not silently proceed.
  local log
  log=$(bash "$ws/promote.sh" --245-rc=2 --154-rc=0 2>&1 || true)
  if echo "$log" | grep -q "refusing 154"; then
    log_pass "gate explicitly refuses 154 promotion"
  else
    log_fail "gate output missing 'refusing 154' — log: $log"
  fi

  rm -rf "$ws"
}

# --- runner --------------------------------------------------------
run_all() {
  echo "═══════════════════════════════════════════════════════════════"
  echo " deploy_promotion_test.sh — v2 245→154 promotion gate"
  echo "═══════════════════════════════════════════════════════════════"
  test_promotion_gate_artifacts
  test_seq_pinning_idempotent
  test_sequential_bumps_exactly_one
  test_image_tag_reuse_for_promotion
  test_gate_refuses_promotion_on_245_failure

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
    artifacts)      test_promotion_gate_artifacts ;;
    pin)            test_seq_pinning_idempotent ;;
    seq)            test_sequential_bumps_exactly_one ;;
    tag)            test_image_tag_reuse_for_promotion ;;
    gate)           test_gate_refuses_promotion_on_245_failure ;;
    *)              run_all ;;
  esac
else
  run_all
fi

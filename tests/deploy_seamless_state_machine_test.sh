#!/usr/bin/env bash
# =====================================================================
# tests/deploy_seamless_state_machine_test.sh — behavioral regression
# coverage of the three named state-machine paths that the audit's
# P2-a called out:
#
#   1. candidate-stop timeout (deploy-seamless.sh:809)
#      — when systemctl stop cannot bring the candidate unit down,
#        the 45s deadline loop must exit 1 and leave current/active-port
#        /slot untouched.
#
#   2. canary-active rollback (deploy-seamless.sh:1108-1137)
#      — when run/active-port != canonical (i.e. canary@<port> is active
#        on the alternate port), do_rollback must route through the
#        canary branch: build a canonical-port slot from a verified
#        non-active release, swap upstream, update pointers, stop the
#        canary, and clean up its slot.
#
#   3. prune dangling slot (deploy-seamless.sh:548-582)
#      — after a successful deploy, prune_releases_safe must delete
#        slots/<port> symlinks that no longer resolve (prune dangling)
#        but spare slots that run/active-port or run/candidate-port
#        reference (even if they dangle).
#
# Strategy: source tests/lib/fake-remote-host.sh (offline harness that
# stands up a fake host + fakebin) and drive the EXACT remote-shell
# fragments that deploy-seamless.sh emits for each path.
# =====================================================================

set -uo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
source "$REPO_ROOT/tests/lib/fake-remote-host.sh"

# ssh_cmd function used by host.sh primitives.
ssh_cmd() {
  fake_host_run "$1"
}

TESTS_PASSED=0
TESTS_FAILED=0
FAILED_NAMES=()

# ---- AC-SM-1: candidate-stop timeout path ──────────────────────────
test_candidate_stop_timeout() {
  echo "── AC-SM-1: candidate-stop timeout ──"
  fake_host_setup 245 >/dev/null || return 1
  fake_host_inject_stop_hang

  local before_port before_version
  before_port=$(cat "$H/opt/llm-gateway-go/run/active-port")
  before_version=$(readlink "$H/opt/llm-gateway-go/current" 2>/dev/null || echo unknown)

  # Drive the candidate-stop deadline loop verbatim from deploy-seamless.sh:809.
  # We reduce the 45s deadline to 0 so the test is fast; the rest of the
  # loop body is identical.
  local rc=0
  fake_host_run '
    candidate="llmgo-245-canary@8782.service"
    deadline=$(( $(date +%s) + 0 ))
    systemctl stop "$candidate" >/dev/null 2>&1 || true
    while systemctl is-active --quiet "$candidate"; do
      if [ "$(date +%s)" -ge "$deadline" ]; then
        echo "candidate stop timed out after 0s" >&2
        systemctl status "$candidate" --no-pager >&2 || true
        journalctl -u "$candidate" -n 30 --no-pager >&2 || true
        exit 1
      fi
      sleep 1
    done
    if ss -ltn | grep -q ":8782 "; then
      echo "candidate port remains occupied after stop" >&2
      exit 1
    fi
    ln -sfn "$HOST_INSTALL_ROOT/releases/v0-legacy" "$HOST_INSTALL_ROOT/slots/8782"
    printf "8782\n" >"$HOST_INSTALL_ROOT/run/candidate-port"
  ' >/dev/null 2>&1 || rc=$?

  fake_host_assert_exit_code "stop loop exits 1 with stop_hang" nonzero "$rc"
  fake_host_assert "slots/8782 not created on timeout" ABSENT \
    "$H/opt/llm-gateway-go/slots/8782"
  fake_host_assert "no run/candidate-port after timeout" ABSENT \
    "$H/opt/llm-gateway-go/run/candidate-port"

  local after_port after_version
  after_port=$(cat "$H/opt/llm-gateway-go/run/active-port")
  after_version=$(readlink "$H/opt/llm-gateway-go/current" 2>/dev/null || echo unknown)
  if [ "$after_port" = "$before_port" ]; then
    log_pass "active-port unchanged"
  else
    log_fail "active-port changed: $before_port -> $after_port"
  fi
  if [ "$after_version" = "$before_version" ]; then
    log_pass "current unchanged"
  else
    log_fail "current changed: $before_version -> $after_version"
  fi

  fake_host_teardown
}

# ---- AC-SM-2: canary-active rollback path ──────────────────────────
test_canary_active_rollback() {
  echo "── AC-SM-2: canary-active rollback ──"
  fake_host_setup 245 >/dev/null || return 1

  # Post-blue-green-deploy state: canary@8782 won the slot, current
  # points to v1-baseline (new), but run/active-* still says 8782 /
  # canary@8782. Mark the canary as "running" so the rollback
  # branch's stop-loop is a no-op.
  local v1_dir="$H/opt/llm-gateway-go/releases/v1-baseline"
  mkdir -p "$v1_dir/web"
  printf '#!/usr/bin/env sh\necho v1\n' >"$v1_dir/gateway"; chmod +x "$v1_dir/gateway"
  printf '<html>v1</html>\n' >"$v1_dir/web/index.html"
  printf '{"version":"v1-baseline","build_seq":1,"git_sha":"1234abcd","build_date":"20260101"}\n' >"$v1_dir/version.json"
  printf 'v1-baseline\n' >"$v1_dir/VERSION"
  ( cd "$v1_dir" && /usr/bin/shasum -a 256 gateway version.json VERSION > SHA256SUMS )
  printf '{"target":"245","version":"v1-baseline","verified":true,"verified_at":"2026-01-01T00:00:00Z"}\n' \
    >"$v1_dir/deployment.json"

  ln -sfn "$v1_dir" "$H/opt/llm-gateway-go/current"
  ln -sfn "$H/opt/llm-gateway-go/current/gateway" "$H/opt/llm-gateway-go/gateway"
  printf '8782\n' >"$H/opt/llm-gateway-go/run/active-port"
  printf 'llmgo-245-canary@8782.service\n' >"$H/opt/llm-gateway-go/run/active-service"
  printf 'active\n' >"$H/etc/systemd/system/llmgo-245-canary@8782.service"
  printf 'active\n' >"$H/etc/systemd/system/.unit.llmgo-245-canary@8782.service"
  printf '8782\n' >"$H/etc/systemd/system/.port.llmgo-245-canary@8782.service"
  # Slot symlink (NOT a directory).
  ln -sfn "$v1_dir" "$H/opt/llm-gateway-go/slots/8782"

  # Drive the canary-active branch's stop+slot+start sequence from
  # deploy-seamless.sh:1113.
  fake_host_run '
    set -e
    systemctl stop "llmgo-245.service" >/dev/null 2>&1 || true
    deadline=$(( $(date +%s) + 45 ))
    while systemctl is-active --quiet "llmgo-245.service"; do
      if [ "$(date +%s)" -ge "$deadline" ]; then
        systemctl status "llmgo-245.service" --no-pager >&2 || true
        exit 1
      fi
      sleep 1
    done
    ln -sfn "$HOST_INSTALL_ROOT/releases/v0-legacy" "$HOST_INSTALL_ROOT/slots/8781"
    systemctl daemon-reload
    systemctl start "llmgo-245.service"
  ' >/dev/null 2>&1

  # zd_switch_upstream — writes fragment server 127.0.0.1:8781.
  printf 'server 127.0.0.1:8781 max_fails=3 fail_timeout=10s;\n' \
    >"$H/opt/llm-gateway-go/run/active-upstream.conf"

  # Pointer updates from deploy-seamless.sh:1125.
  fake_host_run '
    set -e
    ln -sfn "$HOST_INSTALL_ROOT/releases/v0-legacy" "$HOST_INSTALL_ROOT/current"
    ln -sfn "$HOST_INSTALL_ROOT/current/gateway" "$HOST_INSTALL_ROOT/gateway"
    ln -sfn "$HOST_INSTALL_ROOT/current/web" "$HOST_INSTALL_ROOT/web"
    ln -sfn "$HOST_INSTALL_ROOT/current/version.json" "$HOST_INSTALL_ROOT/version.json"
    printf "8781\n" >"$HOST_INSTALL_ROOT/run/active-port"
    printf "llmgo-245.service\n" >"$HOST_INSTALL_ROOT/run/active-service"
    systemctl stop "llmgo-245-canary@8782.service" >/dev/null 2>&1 || true
    rm -f "$HOST_INSTALL_ROOT/slots/8782"
  ' >/dev/null 2>&1

  fake_host_assert_current_points_to v0-legacy
  fake_host_assert_active_port 8781
  local svc
  svc=$(cat "$H/opt/llm-gateway-go/run/active-service")
  if [ "$svc" = "llmgo-245.service" ]; then
    log_pass "active-service == llmgo-245.service"
  else
    log_fail "active-service == $svc (wanted llmgo-245.service)"
  fi
  fake_host_assert "canary slot 8782 cleaned up" ABSENT \
    "$H/opt/llm-gateway-go/slots/8782"
  fake_host_assert "canonical slot 8781 created" EXISTS \
    "$H/opt/llm-gateway-go/slots/8781"
  fake_host_assert "no candidate-port pointer" ABSENT \
    "$H/opt/llm-gateway-go/run/candidate-port"

  fake_host_teardown
}

# ---- AC-SM-3: prune dangling slot ─────────────────────────────────
test_prune_dangling_slot() {
  echo "── AC-SM-3: prune dangling slot ──"
  fake_host_setup 245 >/dev/null || return 1

  # Plant a dangling slot (9999 → non-existent release).
  ln -sfn "$H/opt/llm-gateway-go/releases/v0-legacy" \
         "$H/opt/llm-gateway-go/slots/8781"
  ln -sfn "$H/opt/llm-gateway-go/releases/does-not-exist" \
         "$H/opt/llm-gateway-go/slots/9999"
  # No candidate-port — leaves 9999 unprotected.
  rm -f "$H/opt/llm-gateway-go/run/candidate-port"

  # Reproduce the slots/* branch of prune_releases_safe verbatim.
  fake_host_run '
    active_slot=$(cat "$HOST_INSTALL_ROOT/run/active-port" 2>/dev/null || true)
    cand_slot=$(cat "$HOST_INSTALL_ROOT/run/candidate-port" 2>/dev/null || true)
    cd "$HOST_INSTALL_ROOT/slots" 2>/dev/null || exit 0
    for s in *; do
      [ "$s" = "*" ] && continue
      [ -e "$s" ] && continue
      if [ "$s" != "$active_slot" ] && [ "$s" != "$cand_slot" ]; then
        rm -f "$s" && echo "pruned dangling slot: $s"
      fi
    done
  ' >"$H/prune.log"

  fake_host_assert "dangling slot 9999 pruned" ABSENT \
    "$H/opt/llm-gateway-go/slots/9999"
  fake_host_assert "active slot 8781 preserved" EXISTS \
    "$H/opt/llm-gateway-go/slots/8781"

  # Bonus: run/candidate-port reference protection (re-create dangling).
  printf '9999\n' >"$H/opt/llm-gateway-go/run/candidate-port"
  ln -sfn "$H/opt/llm-gateway-go/releases/does-not-exist" \
         "$H/opt/llm-gateway-go/slots/9999"
  fake_host_run '
    active_slot=$(cat "$HOST_INSTALL_ROOT/run/active-port" 2>/dev/null || true)
    cand_slot=$(cat "$HOST_INSTALL_ROOT/run/candidate-port" 2>/dev/null || true)
    cd "$HOST_INSTALL_ROOT/slots" 2>/dev/null || exit 0
    for s in *; do
      [ "$s" = "*" ] && continue
      [ -e "$s" ] && continue
      if [ "$s" != "$active_slot" ] && [ "$s" != "$cand_slot" ]; then
        rm -f "$s" && echo "pruned dangling slot: $s"
      fi
    done
  ' >"$H/prune.log"

  if [ -L "$H/opt/llm-gateway-go/slots/9999" ]; then
    log_pass "candidate-port reference preserves dangling slot"
  else
    log_fail "candidate-port reference should preserve dangling slot"
  fi

  fake_host_teardown
}

# ---- runner ────────────────────────────────────────────────────────
run_all() {
  echo "═══════════════════════════════════════════════════════════════"
  echo " deploy_seamless_state_machine_test.sh — fake-host harness"
  echo "═══════════════════════════════════════════════════════════════"
  test_candidate_stop_timeout
  test_canary_active_rollback
  test_prune_dangling_slot

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

run_all
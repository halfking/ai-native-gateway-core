#!/usr/bin/env bash
set -uo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
WRAPPER="$REPO_ROOT/scripts/ssh-wrapper-154.sh"
AUDIT="$REPO_ROOT/scripts/audit-36h.sh"

passed=0
failed=0
pass() { printf 'PASS %s\n' "$1"; passed=$((passed + 1)); }
fail() { printf 'FAIL %s\n' "$1"; failed=$((failed + 1)); }

out=$(env -i PATH="$PATH" bash "$WRAPPER" root@example.invalid true 2>&1)
rc=$?
if [[ $rc -eq 64 ]] && [[ $out == *"SSHPASS_154"* ]]; then
  pass "wrapper rejects missing injected contract"
else
  fail "wrapper missing-contract result rc=$rc out=$out"
fi

if grep -q 'PATH="$SCRIPT_DIR:$PATH"' "$AUDIT" && grep -q 'SSH_BASE_154="ssh -o ConnectTimeout=8 root@\$HOST_154"' "$AUDIT"; then
  pass "audit uses wrapper-aware SSH path"
else
  fail "audit does not use wrapper-aware SSH path"
fi

if grep -q 'journalctl -u llm-gateway-go --since "36 hours ago" --no-pager 2>&1.*> "\$REPORT_DIR/log-154-36h.txt"' "$AUDIT" && ! grep -q '/tmp/log-154-36h.txt\|root@\$HOST_154:/tmp\|scp .*HOST_154' "$AUDIT"; then
  pass "audit streams journal locally without remote staging"
else
  fail "audit still stages or copies 154 logs remotely"
fi

if grep -q ': "\${SSH_WRAPPER_HOP_KEY:?' "$AUDIT" && grep -q ': "\${SSHPASS_154:?' "$AUDIT"; then
  pass "audit fails closed without wrapper injection"
else
  fail "audit lacks required wrapper contract checks"
fi

printf 'summary: %d passed, %d failed\n' "$passed" "$failed"
(( failed == 0 ))

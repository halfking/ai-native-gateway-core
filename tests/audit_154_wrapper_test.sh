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
if [[ $rc -eq 64 ]] && [[ $out == *"target key or explicit password"* ]]; then
  pass "wrapper rejects missing injected key contract"
else
  fail "wrapper missing-contract result rc=$rc out=$out"
fi

capture_dir=$(mktemp -d)
trap 'rm -rf "$capture_dir"' EXIT
cat > "$capture_dir/ssh" <<'EOF'
#!/usr/bin/env bash
printf '%s\n' "$*" > "${WRAPPER_CAPTURE:?}"
EOF
chmod +x "$capture_dir/ssh"
WRAPPER_CAPTURE="$capture_dir/capture" \
  SSH_WRAPPER_HOP_KEY=/keys/hop \
  SSH_WRAPPER_HOP_HOST=115.29.212.252 \
  SSH_WRAPPER_TARGET_KEY=/keys/target \
  SSH_WRAPPER_TARGET_IP=47.97.111.154 \
  SSH_WRAPPER_TARGET_HOST=47.97.111.154 \
  SSH_BIN="$capture_dir/ssh" \
  bash "$WRAPPER" root@47.97.111.154 true
if [[ -f "$capture_dir/capture" ]] && grep -q -- '-i /keys/target' "$capture_dir/capture" && grep -q -- 'ProxyCommand=.*/keys/hop.*115.29.212.252' "$capture_dir/capture" && grep -q 'root@47.97.111.154 true' "$capture_dir/capture"; then
  pass "wrapper routes key-authenticated target through configured hop"
else
  fail "wrapper did not use target key and hop proxy"
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

if grep -q 'SSH_WRAPPER_HOP_KEY' "$AUDIT" && grep -q 'SSH_WRAPPER_TARGET_IP' "$AUDIT" && grep -q 'SSHPASS_154' "$AUDIT" && grep -q 'SSH_WRAPPER_TARGET_KEY' "$AUDIT"; then
  pass "audit checks key-based wrapper contract with password compatibility"
else
  fail "audit lacks key-based wrapper contract checks"
fi

printf 'summary: %d passed, %d failed\n' "$passed" "$failed"
(( failed == 0 ))

#!/usr/bin/env bash
# e2e: 8 scenarios for unlock-remote.sh
# Uses LOCK_REMOTE_SSH_CMD='bash <stub>' so it works across bash process
# boundaries (export -f does not survive into the helper's subshell).
set -uo pipefail
cd /Users/xutaohuang/workspace/official-deploy/services/llm-gateway-go
HELPER=./scripts/deploy-lib/unlock-remote.sh

mkdir -p /tmp/unlock-remote-e2e/root/var/lib/llm-gateway-go
cat > /tmp/unlock-remote-e2e/stub.sh <<'STUB'
#!/usr/bin/env bash
CMD=$1
EFFECTIVE=$(printf '%s' "$CMD" | sed "s|/var/lib/llm-gateway-go|/tmp/unlock-remote-e2e/root/var/lib/llm-gateway-go|g")
bash -c "$EFFECTIVE"
STUB
chmod +x /tmp/unlock-remote-e2e/stub.sh
export LOCK_REMOTE_SSH_CMD="bash /tmp/unlock-remote-e2e/stub.sh"
LOCK_PATH=/tmp/unlock-remote-e2e/root/var/lib/llm-gateway-go/deploy.lock

PASS=0; FAIL=0
check() {
  local name=$1 expected=$2 actual=$3
  if [[ "$actual" == "$expected" ]]; then echo "  ✓ $name"; PASS=$((PASS+1))
  else echo "  ✗ $name (expected=$expected actual=$actual)"; FAIL=$((FAIL+1)); fi
}
run_helper() {
  local rc
  set +e; bash "$HELPER" "$@" >/dev/null 2>&1; rc=$?; set -e
  echo "$rc"
}
reset() { rm -rf "$LOCK_PATH"; }

write_meta() {
  mkdir -p "$LOCK_PATH"
  cat > "$LOCK_PATH/metadata" <<META
target=$1
source_user=root
source_host=deploy-runner-01
pid=12345
started_at=2026-08-28T01:00:00Z
commit=abc1234
version=2.5.0
META
}

echo "=== S1: no lock → 0 ==="
reset
check "S1 exit" 0 "$(run_helper 154)"
test ! -d "$LOCK_PATH" && check "S1 untouched" yes yes || check "S1 untouched" yes no

echo "=== S2: lock no metadata, no --force → 1 ==="
reset; mkdir -p "$LOCK_PATH"
check "S2 exit" 1 "$(run_helper 154)"
test -d "$LOCK_PATH" && check "S2 preserved" yes yes || check "S2 preserved" yes no

echo "=== S3: lock no metadata, --force → 0 (removed) ==="
reset; mkdir -p "$LOCK_PATH"
check "S3 exit" 0 "$(run_helper 154 --force)"
test ! -d "$LOCK_PATH" && check "S3 removed" yes yes || check "S3 removed" yes no

echo "=== S4: valid meta, no --force → 1 (refused) ==="
reset; write_meta 154
check "S4 exit" 1 "$(run_helper 154)"
test -d "$LOCK_PATH" && check "S4 preserved" yes yes || check "S4 preserved" yes no

echo "=== S5: valid meta, --force → 0 (removed) ==="
reset; write_meta 154
check "S5 exit" 0 "$(run_helper 154 --force)"
test ! -d "$LOCK_PATH" && check "S5 removed" yes yes || check "S5 removed" yes no

echo "=== S6: cross-target meta, --force → 1 (refused) ==="
reset; write_meta 245
check "S6 exit" 1 "$(run_helper 154 --force)"
test -d "$LOCK_PATH" && check "S6 preserved" yes yes || check "S6 preserved" yes no

echo "=== S7: no target → 2 ==="
check "S7 exit" 2 "$(run_helper)"

echo "=== S8: unknown target → 2 ==="
check "S8 exit" 2 "$(run_helper 999)"

echo ""
echo "PASS=$PASS FAIL=$FAIL"
[[ $FAIL -eq 0 ]] && exit 0 || exit 1

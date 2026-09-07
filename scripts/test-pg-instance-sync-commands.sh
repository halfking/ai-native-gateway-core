#!/usr/bin/env bash
# Test expanded CLI commands for pg-instance-sync.sh
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SYNC="$ROOT/scripts/pg-instance-sync.sh"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

echo "Testing CLI command validation..."

# Test 1: help command
echo "Test 1: Help command..."
"$SYNC" --help | grep -q "Usage:"
echo "✓ Help command works"

# Test 2: Unknown command fails
echo "Test 2: Unknown command fails..."
if "$SYNC" invalid-command 2>/dev/null; then
    echo "ERROR: Unknown command should fail" >&2
    exit 1
fi
echo "✓ Unknown command properly rejected"

# Test 3: Inventory requires --output-dir
echo "Test 3: Inventory validation..."
if "$SYNC" inventory 2>/dev/null; then
    echo "ERROR: inventory should require --output-dir" >&2
    exit 1
fi
echo "✓ Inventory requires --output-dir"

# Test 4: Plan command backward compatibility
echo "Test 4: Plan command compatibility..."
cat >"$tmp/local.tsv" <<'EOF'
acc_db
test_db
llm_gateway
local_business
EOF

cat >"$tmp/remote.tsv" <<'EOF'
acc_db
llm_gateway
EOF

cat >"$tmp/policy.conf" <<'EOF'
EXCLUDE_DB_REGEX='(^|_)(test|e2e|bench|dev)($|_)|^llm_gateway_sync_'
SCHEMA_ONLY_DBS='llm_gateway'
DB_ALIASES=''
LOCAL_ONLY_ALLOWLIST='local_business'
EOF

"$SYNC" plan \
  --local-inventory "$tmp/local.tsv" \
  --remote-inventory "$tmp/remote.tsv" \
  --policy "$tmp/policy.conf" \
  --manifest "$tmp/manifest.tsv"

# Verify expected classifications including allowlist enforcement
grep -Fxq $'COMMON\tacc_db\tacc_db\tSCHEMA_AND_INSERT_ONLY' "$tmp/manifest.tsv"
grep -Fxq $'EXCLUDED\ttest_db\t-\tNAME_POLICY' "$tmp/manifest.tsv"
grep -Fxq $'COMMON\tllm_gateway\tllm_gateway\tSCHEMA_ONLY' "$tmp/manifest.tsv"
grep -Fxq $'LOCAL_ONLY\tlocal_business\t-\tCREATE_REMOTE_AND_INSERT_ONLY' "$tmp/manifest.tsv"

echo "✓ Plan command works with allowlist enforcement"

# Test 5: Backup is implemented but remains guarded.
echo "Test 5: Backup requires explicit confirmation..."
error_output=$("$SYNC" backup 2>&1 || true)
echo "$error_output" | grep -q "backup requires --yes"
echo "✓ Backup guard is enforced"

# Test 6: Writer/verify commands require their guard flags
echo "Test 6: Guarded commands require flags..."
for cmd in apply-schema apply-data; do
  error_output=$("$SYNC" "$cmd" 2>&1 || true)
  echo "$error_output" | grep -q "requires --yes"
done
error_output=$("$SYNC" verify 2>&1 || true)
echo "$error_output" | grep -q "verify requires readable --manifest"
error_output=$("$SYNC" restore 2>&1 || true)
echo "$error_output" | grep -q "restore requires --yes"
error_output=$("$SYNC" all --yes 2>&1 || true)
echo "$error_output" | grep -q "intentionally not auto-run"
echo "✓ Guarded commands stay fail-closed without required flags"

echo "All CLI tests passed"
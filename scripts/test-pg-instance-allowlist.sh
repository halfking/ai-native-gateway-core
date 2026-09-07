#!/usr/bin/env bash
# Test LOCAL_ONLY_ALLOWLIST enforcement
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SYNC="$ROOT/scripts/pg-instance-sync.sh"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

echo "Testing LOCAL_ONLY_ALLOWLIST enforcement..."

# Create test inventories
cat >"$tmp/local.tsv" <<'EOF'
acc_db
allowed_db
not_allowed_db
test_db
llm_gateway
EOF

cat >"$tmp/remote.tsv" <<'EOF'
acc_db
llm_gateway
EOF

# Test 1: With allowlist - only allowed local-only DB should get CREATE_REMOTE
echo "Test 1: Allowlist enforcement..."
cat >"$tmp/policy-with-allowlist.conf" <<'EOF'
EXCLUDE_DB_REGEX='(^|_)(test|e2e|bench|dev)($|_)|^llm_gateway_sync_'
SCHEMA_ONLY_DBS='llm_gateway'
DB_ALIASES=''
LOCAL_ONLY_ALLOWLIST='allowed_db'
EOF

"$SYNC" plan \
  --local-inventory "$tmp/local.tsv" \
  --remote-inventory "$tmp/remote.tsv" \
  --policy "$tmp/policy-with-allowlist.conf" \
  --manifest "$tmp/manifest-allowlist.tsv"

# Verify classifications
grep -Fxq $'COMMON\tacc_db\tacc_db\tSCHEMA_AND_INSERT_ONLY' "$tmp/manifest-allowlist.tsv"
grep -Fxq $'LOCAL_ONLY\tallowed_db\t-\tCREATE_REMOTE_AND_INSERT_ONLY' "$tmp/manifest-allowlist.tsv"
grep -Fxq $'EXCLUDED\tnot_allowed_db\t-\tNOT_ALLOWLISTED' "$tmp/manifest-allowlist.tsv"
grep -Fxq $'EXCLUDED\ttest_db\t-\tNAME_POLICY' "$tmp/manifest-allowlist.tsv"
grep -Fxq $'COMMON\tllm_gateway\tllm_gateway\tSCHEMA_ONLY' "$tmp/manifest-allowlist.tsv"

echo "✓ LOCAL_ONLY_ALLOWLIST properly enforced"

# Test 2: Empty allowlist - all local-only should be excluded
echo "Test 2: Empty allowlist enforcement..."
cat >"$tmp/policy-no-allowlist.conf" <<'EOF'
EXCLUDE_DB_REGEX='(^|_)(test|e2e|bench|dev)($|_)|^llm_gateway_sync_'
SCHEMA_ONLY_DBS='llm_gateway'
DB_ALIASES=''
LOCAL_ONLY_ALLOWLIST=''
EOF

"$SYNC" plan \
  --local-inventory "$tmp/local.tsv" \
  --remote-inventory "$tmp/remote.tsv" \
  --policy "$tmp/policy-no-allowlist.conf" \
  --manifest "$tmp/manifest-no-allowlist.tsv"

# Both local-only DBs should be excluded
grep -Fxq $'EXCLUDED\tallowed_db\t-\tNOT_ALLOWLISTED' "$tmp/manifest-no-allowlist.tsv"
grep -Fxq $'EXCLUDED\tnot_allowed_db\t-\tNOT_ALLOWLISTED' "$tmp/manifest-no-allowlist.tsv"

echo "✓ Empty allowlist properly excludes all local-only databases"

# Test 3: Multiple allowlist entries
echo "Test 3: Multiple allowlist entries..."
cat >"$tmp/policy-multi-allowlist.conf" <<'EOF'
EXCLUDE_DB_REGEX='(^|_)(test|e2e|bench|dev)($|_)|^llm_gateway_sync_'
SCHEMA_ONLY_DBS='llm_gateway'
DB_ALIASES=''
LOCAL_ONLY_ALLOWLIST='allowed_db not_allowed_db'
EOF

"$SYNC" plan \
  --local-inventory "$tmp/local.tsv" \
  --remote-inventory "$tmp/remote.tsv" \
  --policy "$tmp/policy-multi-allowlist.conf" \
  --manifest "$tmp/manifest-multi-allowlist.tsv"

# Both should be allowed now
grep -Fxq $'LOCAL_ONLY\tallowed_db\t-\tCREATE_REMOTE_AND_INSERT_ONLY' "$tmp/manifest-multi-allowlist.tsv"
grep -Fxq $'LOCAL_ONLY\tnot_allowed_db\t-\tCREATE_REMOTE_AND_INSERT_ONLY' "$tmp/manifest-multi-allowlist.tsv"

echo "✓ Multiple allowlist entries work correctly"

echo "All allowlist tests passed"
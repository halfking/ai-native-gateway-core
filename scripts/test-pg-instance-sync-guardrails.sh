#!/usr/bin/env bash
# Test guardrails with real validation (no fake success)
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
GUARDRAILS="$ROOT/scripts/lib/pg-instance-guardrails.sh"
SYNC="$ROOT/scripts/pg-instance-sync.sh"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

echo "Testing guardrails library functions..."

# Test 1: Hash and provenance validation
echo "Test 1: Hash validation..."
printf 'acc_db\n' >"$tmp/local.tsv"
printf 'acc_db\n' >"$tmp/remote.tsv"
cat >"$tmp/policy.conf" <<'EOF'
EXCLUDE_DB_REGEX='never-match'
SCHEMA_ONLY_DBS=''
DB_ALIASES=''
LOCAL_ONLY_ALLOWLIST=''
EOF
"$SYNC" plan --local-inventory "$tmp/local.tsv" \
  --remote-inventory "$tmp/remote.tsv" --policy "$tmp/policy.conf" \
  --manifest "$tmp/test-manifest.tsv" >/dev/null

correct_hash="$(shasum -a 256 "$tmp/test-manifest.tsv" | awk '{print $1}')"

# Should succeed with correct hash
bash "$GUARDRAILS" validate-freshness "$tmp/test-manifest.tsv" \
  "$correct_hash" "$tmp/policy.conf"
echo "✓ Correct hash validation passes"

# Should fail with wrong hash
if bash "$GUARDRAILS" validate-freshness "$tmp/test-manifest.tsv" "wrong_hash" 2>/dev/null; then
    echo "ERROR: Wrong hash should fail" >&2
    exit 1
fi
echo "✓ Wrong hash validation fails"

printf 'new_db\n' >>"$tmp/local.tsv"
if bash "$GUARDRAILS" validate-freshness "$tmp/test-manifest.tsv" \
  "$correct_hash" "$tmp/policy.conf" 2>/dev/null; then
  echo "ERROR: Changed inventory should fail provenance validation" >&2
  exit 1
fi
printf 'acc_db\n' >"$tmp/local.tsv"
echo "✓ Changed inventory is rejected"

# The schema executor must parse and reject an incorrect allowlist hash before
# initializing any database connections.
printf 'public.example\n' >"$tmp/allowlist.txt"
printf 'action\tlocal_database\tremote_database\tkind\tschema\tobject\tsub_name\tlocal_hash\tremote_hash\n' \
  >"$tmp/impact.tsv"
allowlist_error="$(
  bash "$ROOT/scripts/pg-instance-schema-additive.sh" --yes --freshness-check \
    --manifest "$tmp/test-manifest.tsv" --manifest-hash "$correct_hash" \
    --policy "$tmp/policy.conf" --impact-matrix "$tmp/impact.tsv" \
    --work-dir "$tmp/schema-work" --llm-ssot-allowlist "$tmp/allowlist.txt" \
    --llm-ssot-allowlist-hash wrong 2>&1 || true
)"
grep -Fq "LLM SSOT allowlist hash mismatch" <<<"$allowlist_error"
echo "✓ LLM SSOT allowlist hash validation is executable"

# Test 2: LLM Gateway protection for apply-data
echo "Test 2: LLM Gateway data protection..."
cat >"$tmp/llm-data-manifest.tsv" <<'EOF'
classification	local_database	remote_database	mode
COMMON	llm_gateway	llm_gateway	SCHEMA_AND_INSERT_ONLY
EOF

# apply-data should fail with llm_gateway in data mode
if bash "$GUARDRAILS" validate-llm-protection "apply-data" "$tmp/llm-data-manifest.tsv" 2>/dev/null; then
    echo "ERROR: apply-data should fail for llm_gateway data operations" >&2
    exit 1
fi

# Check specific error message
error_msg=$(bash "$GUARDRAILS" validate-llm-protection "apply-data" "$tmp/llm-data-manifest.tsv" 2>&1 || true)
if ! echo "$error_msg" | grep -q "Data operations on llm_gateway databases are permanently prohibited"; then
    echo "ERROR: Wrong error message for llm_gateway protection: $error_msg" >&2
    exit 1
fi
echo "✓ LLM Gateway data operations properly blocked"

# Full bootstrap also copies data and must be blocked in either direction.
cat >"$tmp/llm-bootstrap-manifest.tsv" <<'EOF'
classification	local_database	remote_database	mode
REMOTE_ONLY	-	llm_gateway	BOOTSTRAP_LOCAL_FULL
EOF
if bash "$GUARDRAILS" validate-llm-protection bootstrap \
  "$tmp/llm-bootstrap-manifest.tsv" 2>/dev/null; then
  echo "ERROR: bootstrap should fail for llm_gateway" >&2
  exit 1
fi
echo "✓ LLM Gateway full bootstrap blocked"

# Test 3: LLM Gateway schema operations should be allowed
echo "Test 3: LLM Gateway schema protection..."
cat >"$tmp/llm-schema-manifest.tsv" <<'EOF'
#local_inventory_file	/tmp/llm_gateway_project/inventory.tsv
classification	local_database	remote_database	mode
COMMON	llm_gateway	llm_gateway	SCHEMA_ONLY
EOF

# apply-schema should work with SCHEMA_ONLY mode
bash "$GUARDRAILS" validate-llm-protection "apply-schema" "$tmp/llm-schema-manifest.tsv"
echo "✓ LLM Gateway schema operations allowed"

# Test 4: 'all' command should allow SCHEMA_ONLY llm_gateway
echo "Test 4: 'all' command with SCHEMA_ONLY llm_gateway..."
bash "$GUARDRAILS" validate-llm-protection "all" "$tmp/llm-schema-manifest.tsv"
echo "✓ 'all' command allows SCHEMA_ONLY llm_gateway"

# Test 5: Database name validation
echo "Test 5: Database name validation..."
cat >"$tmp/test-policy.conf" <<'EOF'
EXCLUDE_DB_REGEX='(^|_)(test|e2e|bench|dev)($|_)|^llm_gateway_sync_'
EOF

# Should fail for excluded names
if EXCLUDE_DB_REGEX='(^|_)(test|e2e|bench|dev)($|_)' bash "$GUARDRAILS" validate-db-name "test_db" 2>/dev/null; then
    echo "ERROR: test_db should be excluded" >&2
    exit 1
fi
echo "✓ Database exclusion rules work"

# Test 6: Manifest age validation with policy
echo "Test 6: Manifest age validation..."
cat >"$tmp/age-policy.conf" <<'EOF'
EXCLUDE_DB_REGEX='never-match'
SCHEMA_ONLY_DBS=''
DB_ALIASES=''
LOCAL_ONLY_ALLOWLIST=''
MAX_MANIFEST_AGE_HOURS=0
EOF

printf 'acc_db\n' >"$tmp/old-local.tsv"
printf 'acc_db\n' >"$tmp/old-remote.tsv"
touch -t 202601010000 "$tmp/old-local.tsv" "$tmp/old-remote.tsv" "$tmp/age-policy.conf"
"$SYNC" plan --local-inventory "$tmp/old-local.tsv" \
  --remote-inventory "$tmp/old-remote.tsv" --policy "$tmp/age-policy.conf" \
  --manifest "$tmp/old-manifest.tsv" >/dev/null
old_hash="$(shasum -a 256 "$tmp/old-manifest.tsv" | awk '{print $1}')"

# Should fail due to age
if bash "$GUARDRAILS" validate-freshness "$tmp/old-manifest.tsv" "$old_hash" "$tmp/age-policy.conf" 2>/dev/null; then
    echo "ERROR: Old manifest should fail age check" >&2
    exit 1
fi

# Check error message
age_error=$(bash "$GUARDRAILS" validate-freshness "$tmp/old-manifest.tsv" "$old_hash" "$tmp/age-policy.conf" 2>&1 || true)
if ! echo "$age_error" | grep -q "manifest is too old"; then
    echo "ERROR: Wrong error message for age check: $age_error" >&2
    exit 1
fi
echo "✓ Manifest age validation works with policy"

echo "All guardrails tests passed"
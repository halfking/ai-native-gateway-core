#!/bin/bash
# Test script for sliding window API and credential health checking

set -e

API_BASE="https://llmgo.kxpms.cn"
ADMIN_TOKEN="${ADMIN_TOKEN:-your_admin_token_here}"

echo "=== Testing Sliding Window API ==="
echo ""

# Test 1: Query demo-tokenplan minimax-m3
echo "1. Querying demo-tokenplan minimax-m3 sliding window..."
CREDENTIAL_ID=123  # Replace with actual ID
MODEL="minimax-m3"

response=$(curl -s -X GET \
  "${API_BASE}/api/credentials/sliding-window?credential_id=${CREDENTIAL_ID}&model=${MODEL}&limit=50" \
  -H "Authorization: Bearer ${ADMIN_TOKEN}")

echo "$response" | jq .

# Extract stats
total=$(echo "$response" | jq -r '.stats.total')
success=$(echo "$response" | jq -r '.stats.success')
failed=$(echo "$response" | jq -r '.stats.failed')
failure_rate=$(echo "$response" | jq -r '.stats.failure_rate')

echo ""
echo "Stats Summary:"
echo "  Total entries: $total"
echo "  Success: $success"
echo "  Failed: $failed"
echo "  Failure rate: $failure_rate"

# Check if all requests failed
if [ "$total" -gt 0 ] && [ "$success" -eq 0 ]; then
  echo ""
  echo "⚠️  WARNING: ALL $total requests failed (100% failure rate)"
  echo "   This credential+model should be marked as unavailable!"
  
  # Show error kinds
  echo ""
  echo "Error kinds:"
  echo "$response" | jq -r '.stats.error_kinds'
fi

# Test 2: Check credential_model_bindings state
echo ""
echo "2. Checking credential_model_bindings state..."
echo "   (Run this SQL on 184:)"
echo ""
cat << 'SQL'
SELECT 
  cmb.credential_id,
  pm.raw_model_name,
  cmb.available,
  cmb.unavailable_reason,
  cmb.unavailable_at,
  cmb.admin_protected
FROM credential_model_bindings cmb
JOIN provider_models pm ON pm.id = cmb.provider_model_id
WHERE cmb.credential_id = 123  -- Replace with actual ID
  AND pm.raw_model_name = 'minimax-m3';
SQL

echo ""
echo "3. If available=TRUE but all requests failed, there's a bug in:"
echo "   - credentialhealth.Checker.CheckAndUpdate (should mark degraded)"
echo "   - credentialstate.Writer.WriteOnError (should write unavailable)"
echo ""

# Test 3: Check Redis sliding window data
echo "4. Checking Redis sliding window key..."
echo "   Redis key: llmgw:callhist:${CREDENTIAL_ID}:${MODEL}"
echo "   (Run on 184:)"
echo "   redis-cli LRANGE llmgw:callhist:${CREDENTIAL_ID}:${MODEL} 0 49"

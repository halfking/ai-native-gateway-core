#!/bin/bash
# Test script for credential heatmap API
# Usage: ./test-heatmap-api.sh

set -e

BASE_URL="${BASE_URL:-http://localhost:8782}"
echo "Testing Credential Heatmap API at $BASE_URL"
echo "============================================"

# Get current time and 1 hour ago
TIME_END=$(date -u +"%Y-%m-%dT%H:%M:%SZ")
TIME_START=$(date -u -v-1H +"%Y-%m-%dT%H:%M:%SZ" 2>/dev/null || date -u -d "1 hour ago" +"%Y-%m-%dT%H:%M:%SZ")

echo ""
echo "Test 1: Basic heatmap query (last 1 hour, 1m granularity)"
echo "-----------------------------------------------------------"
curl -s "$BASE_URL/api/credentials/heatmap?time_start=$TIME_START&time_end=$TIME_END&granularity=1m&exclude_self_test=true" \
  -H "Accept: application/json" | jq -C '.' | head -50

echo ""
echo ""
echo "Test 2: Heatmap with 5m granularity"
echo "------------------------------------"
curl -s "$BASE_URL/api/credentials/heatmap?time_start=$TIME_START&time_end=$TIME_END&granularity=5m" \
  -H "Accept: application/json" | jq -C '.meta'

echo ""
echo ""
echo "Test 3: Check response structure"
echo "---------------------------------"
RESPONSE=$(curl -s "$BASE_URL/api/credentials/heatmap?time_start=$TIME_START&time_end=$TIME_END&granularity=5m")
echo "$RESPONSE" | jq -e '.meta.time_start' > /dev/null && echo "✓ meta.time_start present"
echo "$RESPONSE" | jq -e '.meta.granularity' > /dev/null && echo "✓ meta.granularity present"
echo "$RESPONSE" | jq -e '.credentials' > /dev/null && echo "✓ credentials array present"
CRED_COUNT=$(echo "$RESPONSE" | jq '.credentials | length')
echo "✓ Found $CRED_COUNT credentials"

if [ "$CRED_COUNT" -gt 0 ]; then
  echo "$RESPONSE" | jq -e '.credentials[0].credential_id' > /dev/null && echo "✓ credentials[0].credential_id present"
  echo "$RESPONSE" | jq -e '.credentials[0].models' > /dev/null && echo "✓ credentials[0].models present"
  MODEL_COUNT=$(echo "$RESPONSE" | jq '.credentials[0].models | length')
  echo "✓ First credential has $MODEL_COUNT models"
  
  if [ "$MODEL_COUNT" -gt 0 ]; then
    echo "$RESPONSE" | jq -e '.credentials[0].models[0].raw_model_name' > /dev/null && echo "✓ model.raw_model_name present"
    echo "$RESPONSE" | jq -e '.credentials[0].models[0].buckets' > /dev/null && echo "✓ model.buckets present"
    BUCKET_COUNT=$(echo "$RESPONSE" | jq '.credentials[0].models[0].buckets | length')
    echo "✓ First model has $BUCKET_COUNT time buckets"
  fi
fi

echo ""
echo "Test 4: Invalid granularity (should fail)"
echo "------------------------------------------"
HTTP_CODE=$(curl -s -o /dev/null -w "%{http_code}" "$BASE_URL/api/credentials/heatmap?time_start=$TIME_START&time_end=$TIME_END&granularity=invalid")
if [ "$HTTP_CODE" = "400" ]; then
  echo "✓ Returns 400 for invalid granularity"
else
  echo "✗ Expected 400, got $HTTP_CODE"
fi

echo ""
echo "Test 5: Missing time_start (should fail)"
echo "-----------------------------------------"
HTTP_CODE=$(curl -s -o /dev/null -w "%{http_code}" "$BASE_URL/api/credentials/heatmap?time_end=$TIME_END&granularity=1m")
if [ "$HTTP_CODE" = "400" ]; then
  echo "✓ Returns 400 for missing time_start"
else
  echo "✗ Expected 400, got $HTTP_CODE"
fi

echo ""
echo "============================================"
echo "All tests completed!"
echo ""
echo "To test with authentication:"
echo "  export BASE_URL=http://localhost:8782"
echo "  export AUTH_TOKEN=your_jwt_token"
echo "  curl -H \"Authorization: Bearer \$AUTH_TOKEN\" ..."

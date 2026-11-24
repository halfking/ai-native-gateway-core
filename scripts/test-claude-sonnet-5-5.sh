#!/bin/bash
# Test script for claude-sonnet-5-5 through gateway

set -euo pipefail

GATEWAY_URL="http://192.168.31.211:8080"
API_KEY="${LLM_GATEWAY_API_KEY:-}"

if [ -z "$API_KEY" ]; then
    echo "Error: LLM_GATEWAY_API_KEY not set"
    exit 1
fi

echo "Testing claude-sonnet-5-5 through gateway..."
echo "Gateway URL: $GATEWAY_URL"
echo ""

# Test 1: Simple request with thinking enabled
echo "=== Test 1: Request with thinking.type=enabled ==="
response1=$(curl -s -w "\nHTTP_CODE:%{http_code}" -X POST "$GATEWAY_URL/v1/chat/completions" \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $API_KEY" \
  -d '{
    "model": "claude-sonnet-5-5",
    "messages": [
      {"role": "user", "content": "Say hello in one word"}
    ],
    "thinking": {
      "type": "enabled",
      "budget_tokens": 1000
    }
  }')

http_code1=$(echo "$response1" | grep "HTTP_CODE:" | cut -d: -f2)
body1=$(echo "$response1" | grep -v "HTTP_CODE:")

echo "HTTP Code: $http_code1"
echo "Response body:"
echo "$body1" | jq -C '.' 2>/dev/null || echo "$body1"
echo ""

# Test 2: Request with thinking.type=adaptive
echo "=== Test 2: Request with thinking.type=adaptive ==="
response2=$(curl -s -w "\nHTTP_CODE:%{http_code}" -X POST "$GATEWAY_URL/v1/chat/completions" \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $API_KEY" \
  -d '{
    "model": "claude-sonnet-5-5",
    "messages": [
      {"role": "user", "content": "Say hello in one word"}
    ],
    "thinking": {
      "type": "adaptive"
    }
  }')

http_code2=$(echo "$response2" | grep "HTTP_CODE:" | cut -d: -f2)
body2=$(echo "$response2" | grep -v "HTTP_CODE:")

echo "HTTP Code: $http_code2"
echo "Response body:"
echo "$body2" | jq -C '.' 2>/dev/null || echo "$body2"
echo ""

# Test 3: Request without thinking parameter
echo "=== Test 3: Request without thinking parameter ==="
response3=$(curl -s -w "\nHTTP_CODE:%{http_code}" -X POST "$GATEWAY_URL/v1/chat/completions" \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $API_KEY" \
  -d '{
    "model": "claude-sonnet-5-5",
    "messages": [
      {"role": "user", "content": "Say hello in one word"}
    ]
  }')

http_code3=$(echo "$response3" | grep "HTTP_CODE:" | cut -d: -f2)
body3=$(echo "$response3" | grep -v "HTTP_CODE:")

echo "HTTP Code: $http_code3"
echo "Response body:"
echo "$body3" | jq -C '.' 2>/dev/null || echo "$body3"
echo ""

echo "=== Summary ==="
echo "Test 1 (thinking=enabled): HTTP $http_code1"
echo "Test 2 (thinking=adaptive): HTTP $http_code2"
echo "Test 3 (no thinking): HTTP $http_code3"

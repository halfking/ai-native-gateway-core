#!/bin/bash
# Test script for Sessions V2 API endpoints
# Usage: ./test_sessions_v2_api.sh [host] [session_id]

HOST="${1:-http://localhost:8080}"
SESSION_ID="${2:-test-session-123}"
TENANT="${3:-default}"

echo "=== Testing Sessions V2 API ==="
echo "Host: $HOST"
echo "Session ID: $SESSION_ID"
echo "Tenant: $TENANT"
echo ""

# Test 1: Session Detail API
echo "1. Testing GET /api/admin/sessions/detail"
echo "   Request: GET $HOST/api/admin/sessions/detail?session_id=$SESSION_ID&tenant=$TENANT"
curl -s -X GET "$HOST/api/admin/sessions/detail?session_id=$SESSION_ID&tenant=$TENANT" \
  -H "Content-Type: application/json" | jq '.' || echo "Failed or no data"
echo ""
echo ""

# Test 2: Session Detail with turn_no focus
echo "2. Testing GET /api/admin/sessions/detail with turn_no=1"
echo "   Request: GET $HOST/api/admin/sessions/detail?session_id=$SESSION_ID&tenant=$TENANT&turn_no=1"
curl -s -X GET "$HOST/api/admin/sessions/detail?session_id=$SESSION_ID&tenant=$TENANT&turn_no=1" \
  -H "Content-Type: application/json" | jq '.' || echo "Failed or no data"
echo ""
echo ""

# Test 3: Session Detail with pagination
echo "3. Testing GET /api/admin/sessions/detail with limit=5&offset=0"
echo "   Request: GET $HOST/api/admin/sessions/detail?session_id=$SESSION_ID&tenant=$TENANT&limit=5&offset=0"
curl -s -X GET "$HOST/api/admin/sessions/detail?session_id=$SESSION_ID&tenant=$TENANT&limit=5&offset=0" \
  -H "Content-Type: application/json" | jq '.' || echo "Failed or no data"
echo ""
echo ""

# Test 4: Session Summary API
echo "4. Testing POST /api/admin/sessions/summary"
echo "   Request: POST $HOST/api/admin/sessions/summary"
echo "   Body: {\"session_id\":\"$SESSION_ID\",\"tenant\":\"$TENANT\"}"
curl -s -X POST "$HOST/api/admin/sessions/summary" \
  -H "Content-Type: application/json" \
  -d "{\"session_id\":\"$SESSION_ID\",\"tenant\":\"$TENANT\"}" | jq '.' || echo "Failed"
echo ""
echo ""

# Test 5: Session Summary with up_to_turn
echo "5. Testing POST /api/admin/sessions/summary with up_to_turn=3"
echo "   Request: POST $HOST/api/admin/sessions/summary"
echo "   Body: {\"session_id\":\"$SESSION_ID\",\"tenant\":\"$TENANT\",\"up_to_turn\":3}"
curl -s -X POST "$HOST/api/admin/sessions/summary" \
  -H "Content-Type: application/json" \
  -d "{\"session_id\":\"$SESSION_ID\",\"tenant\":\"$TENANT\",\"up_to_turn\":3}" | jq '.' || echo "Failed"
echo ""
echo ""

# Test 6: List recent sessions (existing API)
echo "6. Testing GET /api/admin/sessions/list (existing API for comparison)"
echo "   Request: GET $HOST/api/admin/sessions/list?tenant=$TENANT&limit=5"
curl -s -X GET "$HOST/api/admin/sessions/list?tenant=$TENANT&limit=5" \
  -H "Content-Type: application/json" | jq '.' || echo "Failed"
echo ""
echo ""

echo "=== Tests Complete ==="
echo ""
echo "Next steps:"
echo "1. Check if session_turns table has data:"
echo "   psql -c \"SELECT COUNT(*) FROM gateway.session_turns WHERE ts > NOW() - INTERVAL '1 hour';\""
echo ""
echo "2. Find a real session_id from DB:"
echo "   psql -c \"SELECT session_id, COUNT(*) as turn_count FROM gateway.session_turns WHERE ts > NOW() - INTERVAL '1 hour' GROUP BY session_id LIMIT 5;\""
echo ""
echo "3. Test with real session_id:"
echo "   ./test_sessions_v2_api.sh http://localhost:8080 <real-session-id>"

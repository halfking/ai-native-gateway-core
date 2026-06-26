#!/bin/bash
# Quick local test for the analytics fix
# This script simulates the fix and tests the SQL queries locally

set -e

echo "=== Quick Analytics Fix Test ==="
echo ""

# Check if we're in the right directory
if [ ! -f "relay/request_log_pipeline.go" ]; then
    echo "Error: Please run this script from the project root"
    exit 1
fi

echo "Step 1: Run unit tests"
echo "======================"
go test -v ./relay -run TestApplyAutoRouteFields
if [ $? -eq 0 ]; then
    echo "✓ All unit tests passed"
else
    echo "✗ Unit tests failed"
    exit 1
fi

echo ""
echo "Step 2: Verify code compiles"
echo "============================="
go build -o bin/llm-gateway ./cmd/gateway
if [ $? -eq 0 ]; then
    echo "✓ Code compiles successfully"
else
    echo "✗ Compilation failed"
    exit 1
fi

echo ""
echo "Step 3: Check SQL query logic"
echo "=============================="
cat << 'EOF' > /tmp/test_analytics_query.sql
-- Test the analytics query with different is_auto_request values
-- This simulates the query in admin/analytics.go

-- Create a temp table with test data
CREATE TEMP TABLE test_request_logs (
    request_id TEXT,
    client_model TEXT,
    outbound_model TEXT,
    is_auto_request BOOLEAN,
    task_type TEXT,
    ts TIMESTAMP
);

-- Insert test data
INSERT INTO test_request_logs VALUES
    ('req1', 'gpt-4', 'gpt-4', TRUE, 'coding', NOW()),
    ('req2', 'claude-3', 'claude-3', FALSE, NULL, NOW()),
    ('req3', 'gpt-3.5', 'gpt-3.5', NULL, NULL, NOW()),
    ('req4', NULL, NULL, NULL, NULL, NOW());

-- Test the original query condition (should match req1 and req2, NOT req3)
SELECT 
    'Original Query' as query_type,
    request_id,
    client_model,
    is_auto_request,
    task_type
FROM test_request_logs
WHERE (
    is_auto_request = TRUE
    OR (is_auto_request = FALSE AND client_model IS NOT NULL AND client_model <> '')
)
ORDER BY request_id;

-- Show what gets filtered out
SELECT 
    'Filtered Out' as query_type,
    request_id,
    client_model,
    is_auto_request,
    task_type
FROM test_request_logs
WHERE NOT (
    is_auto_request = TRUE
    OR (is_auto_request = FALSE AND client_model IS NOT NULL AND client_model <> '')
)
ORDER BY request_id;
EOF

echo "SQL query test created at /tmp/test_analytics_query.sql"
echo ""
echo "To test with your database, run:"
echo "  psql \$LLM_GATEWAY_DATABASE_URL -f /tmp/test_analytics_query.sql"
echo ""

echo "=== Test Summary ==="
echo "✓ Unit tests passed"
echo "✓ Code compiles"
echo "✓ SQL test query created"
echo ""
echo "Next steps:"
echo "1. Review the changes:"
echo "   git diff relay/request_log_pipeline.go"
echo "2. Test with database (optional):"
echo "   ./scripts/test_analytics_fix.sh"
echo "3. Start local server and test:"
echo "   ./bin/llm-gateway"
echo "4. Test analytics endpoints:"
echo "   curl http://localhost:8080/api/admin/auto-route/analytics/matrix?window=7d"
echo ""
echo "For detailed instructions, see: ANALYTICS_FIX_SUMMARY.md"

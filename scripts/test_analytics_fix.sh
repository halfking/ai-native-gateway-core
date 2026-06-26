#!/bin/bash
# Test script to verify the analytics fix
# Run this after applying the code and database changes

set -e

echo "=== Analytics Fix Verification Script ==="
echo ""

# Get database URL from .env
if [ -f .env ]; then
    export $(grep -v '^#' .env | xargs)
fi

DB_URL="${LLM_GATEWAY_DATABASE_URL}"

if [ -z "$DB_URL" ]; then
    echo "Error: LLM_GATEWAY_DATABASE_URL not found in .env"
    exit 1
fi

echo "Step 1: Check current is_auto_request distribution"
echo "=================================================="
psql "$DB_URL" -c "
SELECT 
    COUNT(*) as total,
    COUNT(*) FILTER (WHERE is_auto_request = TRUE) as auto_true,
    COUNT(*) FILTER (WHERE is_auto_request = FALSE) as auto_false,
    COUNT(*) FILTER (WHERE is_auto_request IS NULL) as auto_null,
    ROUND(100.0 * COUNT(*) FILTER (WHERE is_auto_request = TRUE) / NULLIF(COUNT(*), 0), 2) as pct_auto,
    ROUND(100.0 * COUNT(*) FILTER (WHERE is_auto_request = FALSE) / NULLIF(COUNT(*), 0), 2) as pct_specified,
    ROUND(100.0 * COUNT(*) FILTER (WHERE is_auto_request IS NULL) / NULLIF(COUNT(*), 0), 2) as pct_null
FROM request_logs 
WHERE ts >= NOW() - INTERVAL '7 days';
"

echo ""
echo "Step 2: Apply database migration (fix NULL values)"
echo "=================================================="
echo "Running migration: 302_fix_is_auto_request_null.sql"
psql "$DB_URL" -f db/migrations/302_fix_is_auto_request_null.sql

echo ""
echo "Step 3: Check is_auto_request distribution after fix"
echo "===================================================="
psql "$DB_URL" -c "
SELECT 
    COUNT(*) as total,
    COUNT(*) FILTER (WHERE is_auto_request = TRUE) as auto_true,
    COUNT(*) FILTER (WHERE is_auto_request = FALSE) as auto_false,
    COUNT(*) FILTER (WHERE is_auto_request IS NULL) as auto_null
FROM request_logs 
WHERE ts >= NOW() - INTERVAL '7 days';
"

echo ""
echo "Step 4: Test analytics matrix query"
echo "===================================="
psql "$DB_URL" -c "
SELECT COUNT(*) as matrix_rows
FROM (
    SELECT COALESCE(NULLIF(outbound_model, ''), client_model) AS row_key,
           COALESCE(NULLIF(task_type, ''), CASE WHEN is_auto_request THEN 'unknown' ELSE '__specified__' END) AS col_key,
           COUNT(*)::float8 AS val
    FROM request_logs
    WHERE ts >= NOW() - INTERVAL '7 days'
      AND COALESCE(NULLIF(outbound_model, ''), client_model) IS NOT NULL
      AND COALESCE(NULLIF(task_type, ''), CASE WHEN is_auto_request THEN 'unknown' ELSE '__specified__' END) IS NOT NULL
      AND (
        is_auto_request = TRUE
        OR (is_auto_request = FALSE AND client_model IS NOT NULL AND client_model <> '')
      )
    GROUP BY row_key, col_key
) sub;
"

echo ""
echo "Step 5: Test analytics flow query (L1->L2)"
echo "==========================================="
psql "$DB_URL" -c "
SELECT COUNT(*) as flow_links
FROM (
    SELECT COALESCE(NULLIF(task_type, ''), CASE WHEN is_auto_request THEN 'unknown' ELSE '__specified__' END) AS src,
           COALESCE(NULLIF(outbound_model, ''), client_model) AS dst,
           COUNT(*)::float8 AS val
    FROM request_logs
    WHERE ts >= NOW() - INTERVAL '7 days'
      AND COALESCE(NULLIF(task_type, ''), CASE WHEN is_auto_request THEN 'unknown' ELSE '__specified__' END) IS NOT NULL
      AND (
        is_auto_request = TRUE
        OR (is_auto_request = FALSE AND client_model IS NOT NULL AND client_model <> '')
      )
    GROUP BY src, dst
) sub;
"

echo ""
echo "Step 6: Check sample data"
echo "========================="
psql "$DB_URL" -c "
SELECT 
    request_id,
    client_model,
    outbound_model,
    is_auto_request,
    task_type,
    auto_profile,
    ts
FROM request_logs 
WHERE ts >= NOW() - INTERVAL '1 hour'
ORDER BY ts DESC
LIMIT 10;
"

echo ""
echo "=== Verification Complete ==="
echo ""
echo "Next steps:"
echo "1. Build and start the local server: make run"
echo "2. Send test requests (both auto and specified model)"
echo "3. Check analytics endpoints:"
echo "   - http://localhost:8080/api/admin/auto-route/analytics/matrix?window=7d&metric=count&row=task_type"
echo "   - http://localhost:8080/api/admin/auto-route/analytics/flow?window=7d"
echo "4. If all tests pass, deploy to server-71"

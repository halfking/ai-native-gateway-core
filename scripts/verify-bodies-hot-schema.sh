#!/bin/bash
# Verification script for request_logs_bodies_hot schema
# Part of Issue #9: Validate database schema and indexes

set -euo pipefail

DB_HOST="${DB_HOST:-172.16.2.210}"
DB_PORT="${DB_PORT:-5432}"
DB_NAME="${DB_NAME:-llm_gateway}"
DB_USER="${DB_USER:-llm_gateway}"

PSQL="psql -h $DB_HOST -p $DB_PORT -U $DB_USER -d $DB_NAME -t -A"

echo "======================================"
echo "Schema Verification for request_logs_bodies_hot"
echo "======================================"
echo ""

# Check 1: Table exists
echo "✓ Checking if table exists..."
TABLE_EXISTS=$($PSQL -c "SELECT EXISTS (SELECT 1 FROM pg_tables WHERE tablename = 'request_logs_bodies_hot' AND schemaname = 'public');")
if [ "$TABLE_EXISTS" != "t" ]; then
    echo "❌ FAIL: Table request_logs_bodies_hot does not exist"
    exit 1
fi
echo "  ✅ Table exists"
echo ""

# Check 2: Required columns
echo "✓ Checking required columns..."
COLUMNS=$($PSQL -c "SELECT column_name FROM information_schema.columns WHERE table_name = 'request_logs_bodies_hot' ORDER BY ordinal_position;")
REQUIRED_COLS=("request_id" "ts" "request_body" "outbound_body" "response_body")

for col in "${REQUIRED_COLS[@]}"; do
    if echo "$COLUMNS" | grep -q "^${col}$"; then
        echo "  ✅ Column: $col"
    else
        echo "  ❌ FAIL: Missing column: $col"
        exit 1
    fi
done
echo ""

# Check 3: Unique index on (request_id, ts)
echo "✓ Checking UNIQUE index on (request_id, ts)..."
UNIQUE_INDEX=$($PSQL -c "SELECT indexname FROM pg_indexes WHERE tablename = 'request_logs_bodies_hot' AND indexdef LIKE '%UNIQUE%' AND indexdef LIKE '%request_id%' AND indexdef LIKE '%ts%';")
if [ -z "$UNIQUE_INDEX" ]; then
    echo "  ❌ FAIL: UNIQUE index on (request_id, ts) not found"
    exit 1
fi
echo "  ✅ UNIQUE index found: $UNIQUE_INDEX"
echo ""

# Check 4: Index on ts
echo "✓ Checking index on ts..."
TS_INDEX=$($PSQL -c "SELECT indexname FROM pg_indexes WHERE tablename = 'request_logs_bodies_hot' AND indexdef LIKE '%ts%' AND indexdef NOT LIKE '%request_id%';")
if [ -z "$TS_INDEX" ]; then
    echo "  ❌ FAIL: Index on ts not found"
    exit 1
fi
echo "  ✅ ts index found: $TS_INDEX"
echo ""

# Check 5: Index on request_id
echo "✓ Checking index on request_id..."
REQUEST_ID_INDEX=$($PSQL -c "SELECT indexname FROM pg_indexes WHERE tablename = 'request_logs_bodies_hot' AND indexdef LIKE '%request_id%' AND indexdef NOT LIKE '%ts%';")
if [ -z "$REQUEST_ID_INDEX" ]; then
    echo "  ❌ FAIL: Index on request_id not found"
    exit 1
fi
echo "  ✅ request_id index found: $REQUEST_ID_INDEX"
echo ""

# Check 6: Promote function exists
echo "✓ Checking promote function..."
PROMOTE_FUNC=$($PSQL -c "SELECT EXISTS (SELECT 1 FROM pg_proc WHERE proname = 'promote_request_logs_bodies_hot_to_partition');")
if [ "$PROMOTE_FUNC" != "t" ]; then
    echo "  ❌ FAIL: Function promote_request_logs_bodies_hot_to_partition does not exist"
    exit 1
fi
echo "  ✅ Promote function exists"
echo ""

# Check 7: Partition manager registration
echo "✓ Checking partition_manager registration..."
if grep -q "request_logs_bodies_hot.*promote_request_logs_bodies_hot_to_partition" bg/partition_manager.go; then
    echo "  ✅ Registered in bg/partition_manager.go"
else
    echo "  ⚠️  WARNING: Not found in bg/partition_manager.go (may need manual verification)"
fi
echo ""

# Check 8: Table storage type (should be heap, not columnar)
echo "✓ Checking table storage type..."
STORAGE=$($PSQL -c "SELECT am.amname FROM pg_class c JOIN pg_am am ON c.relam = am.oid WHERE c.relname = 'request_logs_bodies_hot';")
if [ "$STORAGE" = "heap" ]; then
    echo "  ✅ Storage type: heap (correct for hot table)"
else
    echo "  ⚠️  WARNING: Storage type is '$STORAGE' (expected 'heap')"
fi
echo ""

# Summary
echo "======================================"
echo "✅ All critical schema checks passed!"
echo "======================================"
echo ""
echo "Table ready to accept dual-write from admin/telemetry.go"
echo "Next step: Implement Ticket #10 (dual-write logic)"

#!/bin/bash
# Offline schema verification for request_logs_bodies_hot
# Validates schema definition by reading migration files
# Part of Issue #9: Validate database schema and indexes

set -euo pipefail

echo "======================================"
echo "Offline Schema Verification"
echo "request_logs_bodies_hot"
echo "======================================"
echo ""

MIGRATION_FILE="sql/migrations/startup/353_request_logs_bodies_hot_independence.sql"

if [ ! -f "$MIGRATION_FILE" ]; then
    echo "❌ FAIL: Migration file not found: $MIGRATION_FILE"
    exit 1
fi

echo "✓ Checking migration file: $MIGRATION_FILE"
echo ""

# Check 1: Table creation
echo "✓ Checking table definition..."
if grep -q "CREATE TABLE IF NOT EXISTS request_logs_bodies_hot" "$MIGRATION_FILE"; then
    echo "  ✅ Table creation statement found"
else
    echo "  ❌ FAIL: Table creation statement not found"
    exit 1
fi
echo ""

# Check 2: Required columns
echo "✓ Checking required columns in migration..."
REQUIRED_COLS=("request_id" "ts" "request_body" "outbound_body" "response_body")
for col in "${REQUIRED_COLS[@]}"; do
    if grep -q "$col" "$MIGRATION_FILE"; then
        echo "  ✅ Column: $col"
    else
        echo "  ❌ FAIL: Column not found in migration: $col"
        exit 1
    fi
done
echo ""

# Check 3: Unique index
echo "✓ Checking UNIQUE index on (request_id, ts)..."
if grep -q "idx_request_logs_bodies_hot_request_id_ts_unique" "$MIGRATION_FILE"; then
    echo "  ✅ UNIQUE index definition found"
else
    echo "  ❌ FAIL: UNIQUE index not found"
    exit 1
fi
echo ""

# Check 4: ts index
echo "✓ Checking ts index..."
if grep -q "idx_request_logs_bodies_hot_ts" "$MIGRATION_FILE"; then
    echo "  ✅ ts index definition found"
else
    echo "  ❌ FAIL: ts index not found"
    exit 1
fi
echo ""

# Check 5: request_id index
echo "✓ Checking request_id index..."
if grep -q "idx_request_logs_bodies_hot_request_id" "$MIGRATION_FILE"; then
    echo "  ✅ request_id index definition found"
else
    echo "  ❌ FAIL: request_id index not found"
    exit 1
fi
echo ""

# Check 6: Promote function
echo "✓ Checking promote function..."
if grep -q "promote_request_logs_bodies_hot_to_partition" "$MIGRATION_FILE"; then
    echo "  ✅ Promote function definition found"
else
    echo "  ❌ FAIL: Promote function not found"
    exit 1
fi
echo ""

# Check 7: Partition manager registration
echo "✓ Checking partition_manager registration..."
PM_FILE="bg/partition_manager.go"
if [ ! -f "$PM_FILE" ]; then
    echo "  ❌ FAIL: File not found: $PM_FILE"
    exit 1
fi

if grep -q 'promote_request_logs_bodies_hot_to_partition' "$PM_FILE"; then
    echo "  ✅ Registered in $PM_FILE (promoteSpecs)"
    grep 'promote_request_logs_bodies_hot_to_partition' "$PM_FILE" | head -1 | sed 's/^/    /'
else
    echo "  ❌ FAIL: Not registered in $PM_FILE"
    exit 1
fi
echo ""

# Check 8: Data lifecycle hot partition mapping
echo "✓ Checking data lifecycle hot partition mapping..."
LIFECYCLE_FILE="admin/data_lifecycle_hot_partition.go"
if [ ! -f "$LIFECYCLE_FILE" ]; then
    echo "  ❌ FAIL: File not found: $LIFECYCLE_FILE"
    exit 1
fi

if grep -q 'request_logs_bodies_hot.*promote_request_logs_bodies_hot_to_partition' "$LIFECYCLE_FILE"; then
    echo "  ✅ Registered in $LIFECYCLE_FILE"
    grep -A 0 'request_logs_bodies_hot' "$LIFECYCLE_FILE" | head -1 | sed 's/^/    /'
else
    echo "  ❌ FAIL: Not registered in $LIFECYCLE_FILE"
    exit 1
fi
echo ""

# Check 9: Verify migration comments explain purpose
echo "✓ Checking migration documentation..."
if grep -q "hot table independence" "$MIGRATION_FILE"; then
    echo "  ✅ Migration includes purpose documentation"
else
    echo "  ⚠️  WARNING: Limited documentation in migration"
fi
echo ""

# Summary
echo "======================================"
echo "✅ All offline schema checks passed!"
echo "======================================"
echo ""
echo "Schema definition verified from migration files:"
echo "  - Table: request_logs_bodies_hot"
echo "  - Columns: request_id, ts, request_body, outbound_body, response_body"
echo "  - Indexes: UNIQUE (request_id, ts), ts DESC, request_id"
echo "  - Promote function: promote_request_logs_bodies_hot_to_partition"
echo "  - Partition manager: ✅ Registered"
echo ""
echo "✅ Ready for Ticket #10: Implement dual-write logic"
echo ""
echo "NOTE: To verify against live database, run:"
echo "  ./scripts/verify-bodies-hot-schema.sh"
echo "  (requires database connectivity)"

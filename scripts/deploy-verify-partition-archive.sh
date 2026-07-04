#!/bin/bash
# Deployment verification script for partition archive management feature
# Usage: ./deploy-verify-partition-archive.sh

set -e

echo "=========================================="
echo "Partition Archive Management - Deployment Verification"
echo "=========================================="
echo ""

# Configuration
DB_HOST="${DB_HOST:-localhost}"
DB_USER="${DB_USER:-postgres}"
DB_NAME="${DB_NAME:-llm_gateway}"
API_BASE_URL="${API_BASE_URL:-https://llmgateway.internal.example.com}"
ADMIN_TOKEN="${ADMIN_TOKEN}"

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

function print_step() {
    echo -e "${GREEN}[STEP]${NC} $1"
}

function print_success() {
    echo -e "${GREEN}[✓]${NC} $1"
}

function print_error() {
    echo -e "${RED}[✗]${NC} $1"
}

function print_warning() {
    echo -e "${YELLOW}[!]${NC} $1"
}

# Check prerequisites
print_step "1. Checking prerequisites..."

if [ -z "$ADMIN_TOKEN" ]; then
    print_error "ADMIN_TOKEN environment variable is not set"
    echo "Export your admin token: export ADMIN_TOKEN=your_token_here"
    exit 1
fi
print_success "Admin token is set"

if ! command -v psql &> /dev/null; then
    print_warning "psql not found, skipping database checks"
    SKIP_DB_CHECKS=true
else
    print_success "psql is available"
fi

if ! command -v curl &> /dev/null; then
    print_error "curl is required but not found"
    exit 1
fi
print_success "curl is available"

echo ""

# Database verification
if [ "$SKIP_DB_CHECKS" != "true" ]; then
    print_step "2. Verifying database migration 305 + 317 + 318 + 318b..."
    
    # Check that all 4 archive functions exist (305, 318)
    echo "Checking archive functions..."
    EXPECTED_FUNCS=("archive_request_logs" "archive_request_wal" "archive_routing_decision_log" "archive_credential_model_index")
    for fn in "${EXPECTED_FUNCS[@]}"; do
        FUNC_COUNT=$(psql -h "$DB_HOST" -U "$DB_USER" -d "$DB_NAME" -t -c \
            "SELECT COUNT(*) FROM pg_proc WHERE proname = '$fn';" 2>/dev/null || echo "0")
        if [ "$FUNC_COUNT" -gt 0 ]; then
            print_success "$fn() function exists"
        else
            print_error "$fn() function NOT found"
            echo "Run migration 305 / 318: psql -h \$DB_HOST -U \$DB_USER -d \$DB_NAME -f db/migrations/305_partition_archive_functions.sql db/migrations/318_fix_archive_functions.sql"
            exit 1
        fi
    done
    
    # Check that all 4 archive tables exist
    echo "Checking archive tables..."
    EXPECTED_TABLES=("request_logs_archive" "request_wal_archive" "routing_decision_log_archive" "credential_model_index_archive")
    for tbl in "${EXPECTED_TABLES[@]}"; do
        TABLE_COUNT=$(psql -h "$DB_HOST" -U "$DB_USER" -d "$DB_NAME" -t -c \
            "SELECT COUNT(*) FROM pg_class WHERE relname = '$tbl' AND relnamespace = 'public'::regnamespace;" 2>/dev/null || echo "0")
        if [ "$TABLE_COUNT" -gt 0 ]; then
            print_success "$tbl table exists"
        else
            print_error "$tbl table NOT found"
            exit 1
        fi
    done
    
    # Check credential_model_index is partitioned (migration 317)
    echo "Checking credential_model_index is partitioned..."
    RELKIND=$(psql -h "$DB_HOST" -U "$DB_USER" -d "$DB_NAME" -t -c \
        "SELECT relkind FROM pg_class WHERE relname = 'credential_model_index' AND relnamespace = 'public'::regnamespace;" 2>/dev/null | tr -d ' ')
    if [ "$RELKIND" = "p" ]; then
        print_success "credential_model_index is a partitioned table (relkind=p)"
    else
        print_error "credential_model_index is NOT partitioned (relkind=$RELKIND, expected p). Run migration 317."
        exit 1
    fi
    
    # Check archive partitions are columnar (or heap for request_logs)
    echo "Checking archive access methods..."
    ARCHIVE_AM=$(psql -h "$DB_HOST" -U "$DB_USER" -d "$DB_NAME" -t -c \
        "SELECT am.amname
           FROM pg_class c
           JOIN pg_am am ON c.relam = am.oid
          WHERE c.relname = 'credential_model_index_archive_2026_06'
            AND c.relnamespace = 'public'::regnamespace;" 2>/dev/null | tr -d ' ')
    if [ "$ARCHIVE_AM" = "columnar" ]; then
        print_success "credential_model_index_archive_2026_06 is columnar"
    else
        print_warning "credential_model_index_archive_2026_06 am=$ARCHIVE_AM (expected columnar, but not yet archived)"
    fi
    
    echo ""
else
    print_warning "Skipping database checks (psql not available)"
    echo ""
fi

# API verification
print_step "3. Verifying API endpoints..."

# Check partitions endpoint
echo "Testing GET /api/admin/data-lifecycle/partitions..."
HTTP_CODE=$(curl -s -o /tmp/partition_response.json -w "%{http_code}" \
    -H "Authorization: Bearer $ADMIN_TOKEN" \
    "$API_BASE_URL/api/admin/data-lifecycle/partitions")

if [ "$HTTP_CODE" = "200" ]; then
    print_success "GET /api/admin/data-lifecycle/partitions - OK (200)"
    
    # Parse response
    if command -v jq &> /dev/null; then
        TABLES=$(jq -r '.[].table_name' /tmp/partition_response.json 2>/dev/null)
        echo "  Found tables: $TABLES"
        
        TOTAL_PARTITIONS=$(jq '[.[].total_partitions] | add' /tmp/partition_response.json 2>/dev/null)
        echo "  Total partitions: $TOTAL_PARTITIONS"
        
        ARCHIVABLE=$(jq '[.[].archivable_count] | add' /tmp/partition_response.json 2>/dev/null)
        echo "  Archivable partitions: $ARCHIVABLE"
    fi
else
    print_error "GET /api/admin/data-lifecycle/partitions - Failed ($HTTP_CODE)"
    echo "Response:"
    cat /tmp/partition_response.json
    exit 1
fi

echo ""

# Test dry-run archive for every supported table
print_step "4. Testing dry-run archive for all 4 tables..."

# Get a recent month (3 months ago) for testing
TEST_MONTH=$(date -d '3 months ago' +%Y-%m 2>/dev/null || date -v-3m +%Y-%m 2>/dev/null || echo "2026-03")

TABLES=("request_logs" "request_wal" "routing_decision_log" "credential_model_index")
for TABLE in "${TABLES[@]}"; do
    echo "Testing dry-run archive for $TABLE $TEST_MONTH..."
    HTTP_CODE=$(curl -s -o /tmp/archive_response.json -w "%{http_code}" \
        -X POST \
        -H "Authorization: Bearer $ADMIN_TOKEN" \
        -H "Content-Type: application/json" \
        -d "{\"table_name\":\"$TABLE\",\"archive_month\":\"$TEST_MONTH\",\"dry_run\":true}" \
        "$API_BASE_URL/api/admin/data-lifecycle/partitions/archive")

    if [ "$HTTP_CODE" = "200" ]; then
        print_success "POST .../archive (dry-run, $TABLE) - OK (200)"
        if command -v jq &> /dev/null; then
            STATUS=$(jq -r '.status' /tmp/archive_response.json 2>/dev/null)
            MESSAGE=$(jq -r '.message' /tmp/archive_response.json 2>/dev/null)
            echo "  Status: $STATUS"
            echo "  Message: $MESSAGE"
        fi
    else
        print_error "POST .../archive (dry-run, $TABLE) - Failed ($HTTP_CODE)"
        cat /tmp/archive_response.json
        exit 1
    fi
done

echo ""

# Summary
print_step "5. Deployment Verification Summary"
echo ""
print_success "All checks passed!"
echo ""
echo "Next steps:"
echo "  1. Monitor the partitions status in admin interface"
echo "  2. Review archivable partitions count (4 tables now supported)"
echo "  3. Execute actual archive when ready (remove dry_run flag)"
echo ""
echo "Tables under lifecycle management (migration 318):"
echo "  - request_logs             (ts,        columnar archive)"
echo "  - request_wal              (created_at, columnar archive)"
echo "  - routing_decision_log     (ts,        columnar archive)"
echo "  - credential_model_index   (bucket,    columnar archive, 7d cutoff)"
echo ""
echo "Monthly schedule (cron: 0 4 1-3 * * /opt/scripts/columnar-monthly-cron.sh):"
echo "  day 1: request_logs, routing_decision_log"
echo "  day 2: request_wal"
echo "  day 3: credential_model_index"
echo ""
echo "Documentation:"
echo "  - Quick Start: DATA_LIFECYCLE_PARTITION_README.md"
echo "  - Full Docs: docs/partition/OPERATIONS_RUNBOOK.md §7 HTTP API 归档操作"
echo ""
echo "=========================================="
echo "Verification completed successfully!"
echo "=========================================="

# Cleanup
rm -f /tmp/partition_response.json /tmp/archive_response.json

#!/bin/bash

# AUTO_MODEL V3 - Rollback Script
# Date: 2026-09-02
# Purpose: Rollback database migrations for AUTO_MODEL V3 optimization
# Usage: ./rollback_migrations.sh [local|postgres-252|acc]

set -e  # Exit on error
set -u  # Exit on undefined variable

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# Script directory
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROLLBACK_DIR="${SCRIPT_DIR}/rollback"

# Environment selection
ENV="${1:-local}"

echo -e "${RED}╔════════════════════════════════════════════════════════════╗${NC}"
echo -e "${RED}║     AUTO_MODEL V3 - Database Migration Rollback           ║${NC}"
echo -e "${RED}╠════════════════════════════════════════════════════════════╣${NC}"
echo -e "${RED}║ Environment: ${ENV}${NC}"
echo -e "${RED}║ Date: $(date '+%Y-%m-%d %H:%M:%S')${NC}"
echo -e "${RED}╚════════════════════════════════════════════════════════════╝${NC}"
echo ""

# Database connection parameters
case "$ENV" in
    local)
        DB_HOST="${DB_HOST:-localhost}"
        DB_PORT="${DB_PORT:-5432}"
        DB_NAME="${DB_NAME:-llm_gateway}"
        DB_USER="${DB_USER:-postgres}"
        ;;
    postgres-252)
        DB_HOST="postgres-252"
        DB_PORT="5432"
        DB_NAME="llm_gateway"
        DB_USER="postgres"
        ;;
    acc)
        DB_HOST="postgres-acc"
        DB_PORT="5432"
        DB_NAME="llm_gateway"
        DB_USER="postgres"
        ;;
    *)
        echo -e "${RED}✗ Unknown environment: $ENV${NC}"
        echo "Usage: $0 [local|postgres-252|acc]"
        exit 1
        ;;
esac

echo -e "${YELLOW}⚠ WARNING: This will REMOVE all V3 schema changes!${NC}"
echo -e "${YELLOW}⚠ Data in new columns will be LOST!${NC}"
echo ""
echo -e "${BLUE}Connection: ${DB_USER}@${DB_HOST}:${DB_PORT}/${DB_NAME}${NC}"
echo ""

# Confirm before proceeding
read -p "Are you absolutely sure you want to rollback? (type 'ROLLBACK' to confirm): " CONFIRM
if [ "$CONFIRM" != "ROLLBACK" ]; then
    echo -e "${GREEN}Rollback cancelled.${NC}"
    exit 0
fi

# Test database connection
echo -e "${BLUE}Testing database connection...${NC}"
export PGPASSWORD="${DB_PASSWORD:-}"
if psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" -c "SELECT 1;" > /dev/null 2>&1; then
    echo -e "${GREEN}✓ Database connection successful${NC}"
else
    echo -e "${RED}✗ Cannot connect to database${NC}"
    exit 1
fi

echo ""
echo -e "${RED}════════════════════════════════════════════════════════════${NC}"
echo -e "${RED}Starting Rollback (reverse order)${NC}"
echo -e "${RED}════════════════════════════════════════════════════════════${NC}"
echo ""

# Rollback 3: Remove tier from provider models
echo -e "${YELLOW}[1/3] Rolling back: provider_models tier column${NC}"
if psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" \
    -f "${ROLLBACK_DIR}/202609_03_rollback_tier_to_provider_models.sql"; then
    echo -e "${GREEN}✓ Rollback 3 completed${NC}"
else
    echo -e "${RED}✗ Rollback 3 failed${NC}"
    exit 1
fi
echo ""

# Rollback 2: Drop task type tier config
echo -e "${YELLOW}[2/3] Rolling back: task_type_tier_config table${NC}"
if psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" \
    -f "${ROLLBACK_DIR}/202609_02_rollback_task_type_tier_config.sql"; then
    echo -e "${GREEN}✓ Rollback 2 completed${NC}"
else
    echo -e "${RED}✗ Rollback 2 failed${NC}"
    exit 1
fi
echo ""

# Rollback 1: Remove request type fields
echo -e "${YELLOW}[3/3] Rolling back: request_logs new columns${NC}"
if psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" \
    -f "${ROLLBACK_DIR}/202609_01_rollback_request_type_fields.sql"; then
    echo -e "${GREEN}✓ Rollback 1 completed${NC}"
else
    echo -e "${RED}✗ Rollback 1 failed${NC}"
    exit 1
fi
echo ""

echo -e "${GREEN}╔════════════════════════════════════════════════════════════╗${NC}"
echo -e "${GREEN}║     Rollback Completed Successfully                        ║${NC}"
echo -e "${GREEN}╠════════════════════════════════════════════════════════════╣${NC}"
echo -e "${GREEN}║ Environment: ${ENV}${NC}"
echo -e "${GREEN}║ Timestamp: $(date '+%Y-%m-%d %H:%M:%S')${NC}"
echo -e "${GREEN}║                                                            ║${NC}"
echo -e "${GREEN}║ All V3 schema changes have been removed.                   ║${NC}"
echo -e "${GREEN}║ Database is back to pre-V3 state.                          ║${NC}"
echo -e "${GREEN}╚════════════════════════════════════════════════════════════╝${NC}"
echo ""

# Remove migration records
echo -e "${BLUE}Removing migration records...${NC}"
psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" << EOF
DELETE FROM schema_migrations WHERE version IN ('202609_01', '202609_02', '202609_03');
EOF

echo -e "${GREEN}✓ Migration records removed${NC}"
echo ""

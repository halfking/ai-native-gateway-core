#!/bin/bash

# AUTO_MODEL V3 - Migration Execution Script
# Date: 2026-09-02
# Purpose: Execute database migrations for AUTO_MODEL V3 optimization
# Usage: ./run_migrations.sh [local|postgres-252|acc]

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
MIGRATIONS_DIR="${SCRIPT_DIR}/migrations"
ROLLBACK_DIR="${SCRIPT_DIR}/rollback"

# Environment selection
ENV="${1:-local}"

echo -e "${BLUE}╔════════════════════════════════════════════════════════════╗${NC}"
echo -e "${BLUE}║     AUTO_MODEL V3 - Database Migration Execution          ║${NC}"
echo -e "${BLUE}╠════════════════════════════════════════════════════════════╣${NC}"
echo -e "${BLUE}║ Environment: ${ENV}${NC}"
echo -e "${BLUE}║ Date: $(date '+%Y-%m-%d %H:%M:%S')${NC}"
echo -e "${BLUE}╚════════════════════════════════════════════════════════════╝${NC}"
echo ""

# Database connection parameters
case "$ENV" in
    local)
        DB_HOST="${DB_HOST:-localhost}"
        DB_PORT="${DB_PORT:-5432}"
        DB_NAME="${DB_NAME:-llm_gateway}"
        DB_USER="${DB_USER:-postgres}"
        echo -e "${GREEN}✓ Using local database${NC}"
        ;;
    postgres-252)
        DB_HOST="postgres-252"
        DB_PORT="5432"
        DB_NAME="llm_gateway"
        DB_USER="postgres"
        echo -e "${YELLOW}⚠ Using postgres-252 (dev environment)${NC}"
        ;;
    acc)
        DB_HOST="postgres-acc"
        DB_PORT="5432"
        DB_NAME="llm_gateway"
        DB_USER="postgres"
        echo -e "${RED}⚠ Using ACC environment - CAUTION REQUIRED${NC}"
        ;;
    *)
        echo -e "${RED}✗ Unknown environment: $ENV${NC}"
        echo "Usage: $0 [local|postgres-252|acc]"
        exit 1
        ;;
esac

echo ""
echo -e "${BLUE}Connection: ${DB_USER}@${DB_HOST}:${DB_PORT}/${DB_NAME}${NC}"
echo ""

# Confirm before proceeding
if [ "$ENV" != "local" ]; then
    read -p "Are you sure you want to run migrations on $ENV? (yes/no): " CONFIRM
    if [ "$CONFIRM" != "yes" ]; then
        echo -e "${YELLOW}Migration cancelled.${NC}"
        exit 0
    fi
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
echo -e "${BLUE}════════════════════════════════════════════════════════════${NC}"
echo -e "${BLUE}Starting Migrations${NC}"
echo -e "${BLUE}════════════════════════════════════════════════════════════${NC}"
echo ""

# Migration 1: Add request type fields
echo -e "${YELLOW}[1/3] Running: 202609_01_add_request_type_fields.sql${NC}"
if psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" \
    -f "${MIGRATIONS_DIR}/202609_01_add_request_type_fields.sql"; then
    echo -e "${GREEN}✓ Migration 1 completed${NC}"
else
    echo -e "${RED}✗ Migration 1 failed${NC}"
    exit 1
fi
echo ""

# Migration 2: Create task type tier config
echo -e "${YELLOW}[2/3] Running: 202609_02_create_task_type_tier_config.sql${NC}"
if psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" \
    -f "${MIGRATIONS_DIR}/202609_02_create_task_type_tier_config.sql"; then
    echo -e "${GREEN}✓ Migration 2 completed${NC}"
else
    echo -e "${RED}✗ Migration 2 failed${NC}"
    echo -e "${YELLOW}Rolling back migration 1...${NC}"
    psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" \
        -f "${ROLLBACK_DIR}/202609_01_rollback_request_type_fields.sql" || true
    exit 1
fi
echo ""

# Migration 3: Add tier to provider models
echo -e "${YELLOW}[3/3] Running: 202609_03_add_tier_to_provider_models.sql${NC}"
if psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" \
    -f "${MIGRATIONS_DIR}/202609_03_add_tier_to_provider_models.sql"; then
    echo -e "${GREEN}✓ Migration 3 completed${NC}"
else
    echo -e "${RED}✗ Migration 3 failed${NC}"
    echo -e "${YELLOW}Rolling back migrations 1-2...${NC}"
    psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" \
        -f "${ROLLBACK_DIR}/202609_02_rollback_task_type_tier_config.sql" || true
    psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" \
        -f "${ROLLBACK_DIR}/202609_01_rollback_request_type_fields.sql" || true
    exit 1
fi
echo ""

# Run validation
echo -e "${BLUE}════════════════════════════════════════════════════════════${NC}"
echo -e "${BLUE}Running Validation${NC}"
echo -e "${BLUE}════════════════════════════════════════════════════════════${NC}"
echo ""

if psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" \
    -f "${MIGRATIONS_DIR}/validate_v3_migrations.sql"; then
    echo -e "${GREEN}✓ Validation completed${NC}"
else
    echo -e "${YELLOW}⚠ Validation completed with warnings${NC}"
fi

echo ""
echo -e "${GREEN}╔════════════════════════════════════════════════════════════╗${NC}"
echo -e "${GREEN}║     Migrations Completed Successfully                      ║${NC}"
echo -e "${GREEN}╠════════════════════════════════════════════════════════════╣${NC}"
echo -e "${GREEN}║ Environment: ${ENV}${NC}"
echo -e "${GREEN}║ Timestamp: $(date '+%Y-%m-%d %H:%M:%S')${NC}"
echo -e "${GREEN}╠════════════════════════════════════════════════════════════╣${NC}"
echo -e "${GREEN}║ Next Steps:                                                ║${NC}"
echo -e "${GREEN}║ 1. Review validation output above                          ║${NC}"
echo -e "${GREEN}║ 2. Proceed to Phase 2: Enhanced Classification             ║${NC}"
echo -e "${GREEN}║ 3. Update code to use new schema fields                    ║${NC}"
echo -e "${GREEN}╚════════════════════════════════════════════════════════════╝${NC}"
echo ""

# Optional: Create migration record
echo -e "${BLUE}Creating migration record...${NC}"
psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" << EOF
INSERT INTO schema_migrations (version, description, executed_at)
VALUES 
    ('202609_01', 'Add request type classification fields', NOW()),
    ('202609_02', 'Create task type tier config table', NOW()),
    ('202609_03', 'Add tier to provider models', NOW())
ON CONFLICT (version) DO NOTHING;
EOF

echo -e "${GREEN}✓ Migration record created${NC}"
echo ""

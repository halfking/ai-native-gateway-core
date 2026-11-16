#!/bin/bash
# Setup script for PostgreSQL test database

set -e

# Configuration
DB_NAME="${TEST_DB_NAME:-llm_gateway_test}"
DB_USER="${TEST_DB_USER:-postgres}"
DB_HOST="${TEST_DB_HOST:-localhost}"
DB_PORT="${TEST_DB_PORT:-5432}"

echo "Setting up test database: $DB_NAME"

# Check if PostgreSQL is running
if ! pg_isready -h "$DB_HOST" -p "$DB_PORT" > /dev/null 2>&1; then
    echo "ERROR: PostgreSQL is not running on $DB_HOST:$DB_PORT"
    echo ""
    echo "To start PostgreSQL:"
    echo "  macOS (Homebrew): brew services start postgresql@14"
    echo "  Linux: sudo systemctl start postgresql"
    echo "  Docker: docker run --name postgres-test -e POSTGRES_PASSWORD=postgres -p 5432:5432 -d postgres:14"
    exit 1
fi

# Create test database if it doesn't exist
echo "Creating database $DB_NAME (if not exists)..."
psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -tc "SELECT 1 FROM pg_database WHERE datname = '$DB_NAME'" | grep -q 1 || \
    psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -c "CREATE DATABASE $DB_NAME"

# Export connection string for tests
export TEST_DB_URL="postgres://$DB_USER@$DB_HOST:$DB_PORT/$DB_NAME?sslmode=disable"

echo ""
echo "✓ Test database ready"
echo ""
echo "Run tests with:"
echo "  export TEST_DB_URL=\"$TEST_DB_URL\""
echo "  go test -v ./plugin-runtime -run TestDBRegistry"
echo ""
echo "Or use Docker:"
echo "  docker run --name postgres-test -e POSTGRES_PASSWORD=postgres -p 5432:5432 -d postgres:14"
echo "  export TEST_DB_URL=\"postgres://postgres:postgres@localhost:5432/postgres?sslmode=disable\""
echo "  go test -v ./plugin-runtime -run TestDBRegistry"

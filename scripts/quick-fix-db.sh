#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
: "${DATABASE_URL:?DATABASE_URL must be explicitly set}"
printf '%s\n' 'Applying repository migrations with strict error handling and without dropping tables or seeding credentials.'
exec "$SCRIPT_DIR/run-migrations-strict.sh"

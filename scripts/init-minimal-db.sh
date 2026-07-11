#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
: "${DATABASE_URL:?DATABASE_URL must be explicitly set}"
printf '%s\n' 'Initializing an empty database through repository migrations; no tables or databases will be dropped.'
exec "$SCRIPT_DIR/run-migrations-strict.sh" --bootstrap

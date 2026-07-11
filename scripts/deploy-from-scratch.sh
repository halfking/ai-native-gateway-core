#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
printf '%s\n' 'Non-destructive deployment: existing database objects and data are preserved.'
exec "$SCRIPT_DIR/local-deploy.sh" deploy

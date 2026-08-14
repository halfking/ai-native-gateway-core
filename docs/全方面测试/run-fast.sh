#!/bin/bash
# Deprecated compatibility wrapper. Use the strict run-scoped runner instead.
#
# Usage: bash docs/全方面测试/run-fast.sh [run_all.sh options]

set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
exec bash "$SCRIPT_DIR/scenarios/run_all.sh" --suite functional --fast "$@"

#!/usr/bin/env bash
# ⚠️ 已废弃: 请使用 scripts/local-down.sh（统一入口）
set -euo pipefail
echo "╔══════════════════════════════════════════════════════════════╗"
echo "║ ⚠️  local-r112-down.sh 已废弃                              ║"
echo "║ 请使用: ./scripts/local-down.sh [--volumes|-v]             ║"
echo "╚══════════════════════════════════════════════════════════════╝"
echo ""
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
exec "$SCRIPT_DIR/local-down.sh" "$@"

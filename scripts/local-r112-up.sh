#!/usr/bin/env bash
# ⚠️ 已废弃: 请使用 scripts/local-up.sh（统一入口，功能更全: --deps/--rebuild/--no-smoke/--no-v1）
set -euo pipefail
echo "╔══════════════════════════════════════════════════════════════╗"
echo "║ ⚠️  local-r112-up.sh 已废弃                                ║"
echo "║ 请使用: ./scripts/local-up.sh [--deps|--rebuild|...]       ║"
echo "╚══════════════════════════════════════════════════════════════╝"
echo ""
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
exec "$SCRIPT_DIR/local-up.sh" "$@"

#!/bin/bash
# ⚠️ 已废弃: 请使用 scripts/pg-columnar-rotate.sh --local（已合并）
set -euo pipefail
echo "╔══════════════════════════════════════════════════════════════╗"
echo "║ ⚠️  pg-columnar-rotate-local.sh 已废弃                      ║"
echo "║ 请使用: ./scripts/pg-columnar-rotate.sh --local             ║"
echo "╚══════════════════════════════════════════════════════════════╝"
echo ""
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
exec "$SCRIPT_DIR/pg-columnar-rotate.sh" --local

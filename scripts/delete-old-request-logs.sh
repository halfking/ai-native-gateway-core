#!/bin/bash
# ⚠️ 已废弃: 请使用 scripts/manage-request-logs.sh --delete（已合并）
set -euo pipefail
echo "╔══════════════════════════════════════════════════════════════╗"
echo "║ ⚠️  delete-old-request-logs.sh 已废弃                       ║"
echo "║ 请使用: ./scripts/manage-request-logs.sh --delete           ║"
echo "╚══════════════════════════════════════════════════════════════╝"
echo ""
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
exec "$SCRIPT_DIR/manage-request-logs.sh" --delete

#!/bin/bash
# ⚠️ 已废弃: 请使用 pre-deploy-check.sh --partition-archive（已合并）
set -euo pipefail
echo "╔══════════════════════════════════════════════════════════════╗"
echo "║ ⚠️  deploy-verify-partition-archive.sh 已废弃              ║"
echo "║ 请使用: ./scripts/pre-deploy-check.sh --partition-archive  ║"
echo "╚══════════════════════════════════════════════════════════════╝"
echo ""
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
exec "$SCRIPT_DIR/pre-deploy-check.sh" --partition-archive

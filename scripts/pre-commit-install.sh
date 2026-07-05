#!/usr/bin/env bash
# ⚠️ 已废弃: 请使用 scripts/install-githooks.sh --pre-commit（已合并）
set -euo pipefail
echo "╔══════════════════════════════════════════════════════════════╗"
echo "║ ⚠️  pre-commit-install.sh 已废弃                           ║"
echo "║ 请使用: ./scripts/install-githooks.sh --pre-commit         ║"
echo "╚══════════════════════════════════════════════════════════════╝"
echo ""
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
exec "$SCRIPT_DIR/install-githooks.sh" --pre-commit

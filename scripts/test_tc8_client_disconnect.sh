#!/usr/bin/env bash
# ⚠️ 已废弃: 请使用 scripts/test-runtime-tc.sh --tc8（已合并）
set -euo pipefail
echo "╔══════════════════════════════════════════════════════════════╗"
echo "║ ⚠️  test_tc8_client_disconnect.sh 已废弃                    ║"
echo "║ 请使用: ./scripts/test-runtime-tc.sh --tc8                  ║"
echo "╚══════════════════════════════════════════════════════════════╝"
echo ""
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
exec "$SCRIPT_DIR/test-runtime-tc.sh" --tc8

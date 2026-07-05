#!/usr/bin/env bash
# ⚠️ 已废弃: 请使用 scripts/test-runtime-tc.sh --tc6（已合并）
set -euo pipefail
echo "╔══════════════════════════════════════════════════════════════╗"
echo "║ ⚠️  test_tc6_quota_silent_failover.sh 已废弃                ║"
echo "║ 请使用: ./scripts/test-runtime-tc.sh --tc6                  ║"
echo "╚══════════════════════════════════════════════════════════════╝"
echo ""
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
exec "$SCRIPT_DIR/test-runtime-tc.sh" --tc6

#!/bin/bash
# ⚠️ 已废弃: 请使用 deploy-184.sh --columnar（统一入口）
set -euo pipefail
REPO_DIR="${LLM_GATEWAY_REPO:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
echo "╔══════════════════════════════════════════════════════════════╗"
echo "║ ⚠️  此脚本已废弃                                           ║"
echo "║ 请使用: ./deploy-184.sh --columnar                          ║"
echo "╚══════════════════════════════════════════════════════════════╝"
echo ""
exec "$REPO_DIR/deploy-184.sh" --columnar

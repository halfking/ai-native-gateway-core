#!/usr/bin/env bash
# ⚠️ 已废弃: 请使用 deploy-184.sh --with-migration（统一入口）
set -euo pipefail
REPO_DIR="${LLM_GATEWAY_REPO:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
echo "╔══════════════════════════════════════════════════════════════╗"
echo "║ ⚠️  此脚本已废弃                                           ║"
echo "║ 请使用: ./deploy-184.sh --with-migration                    ║"
echo "╚══════════════════════════════════════════════════════════════╝"
echo ""
exec "$REPO_DIR/deploy-184.sh" --with-migration

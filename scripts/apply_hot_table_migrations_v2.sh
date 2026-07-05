#!/bin/bash
# ⚠️ 已废弃: 请使用 scripts/apply-hot-table-migrations.sh --env <env>
set -euo pipefail
echo "╔══════════════════════════════════════════════════════════════╗"
echo "║ ⚠️  apply_hot_table_migrations_v2.sh 已废弃                ║"
echo "║ 请使用: ./scripts/apply-hot-table-migrations.sh --env <env>║"
echo "╚══════════════════════════════════════════════════════════════╝"
echo ""
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
exec "$SCRIPT_DIR/apply-hot-table-migrations.sh" --env "${1:-local}"

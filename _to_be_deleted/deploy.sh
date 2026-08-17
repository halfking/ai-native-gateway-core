#!/usr/bin/env bash
# =====================================================================
# deploy.sh — 仓库根目录统一部署入口（委托 scripts/deploy.sh）
#
# 用法:
#   ./deploy.sh 154
#   ./deploy.sh 252 --dry-run
#   ./deploy.sh --help
# =====================================================================

set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
exec "$ROOT/scripts/deploy.sh" "$@"

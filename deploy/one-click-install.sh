#!/usr/bin/env bash
# deploy/one-click-install.sh — 整合入口（转发至 deploy/one-click/install.sh）
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
exec bash "$ROOT/one-click/install.sh" "$@"

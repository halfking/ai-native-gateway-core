#!/usr/bin/env bash
# scripts/deploy-71.sh — 71 专用一键部署包装（已与 scripts/deploy.sh 合并）
#
# 推荐: 直接使用 scripts/deploy.sh 71
# 此脚本保留为 thin wrapper 以向后兼容旧命令
#
# 用法:
#   scripts/deploy-71.sh                  # 默认 deploy
#   scripts/deploy-71.sh --dry-run        # 不实际部署
#   SSHPASS=xxx scripts/deploy-71.sh      # 显式提供密码（73+ skill 默认走 ssh-agent）

set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# 透传所有参数给统一的 scripts/deploy.sh
exec bash "$SCRIPT_DIR/deploy.sh" 71 "$@"

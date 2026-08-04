#!/usr/bin/env bash
# =====================================================================
# scripts/deploy-to-154-auto.sh — 154 自动部署（委托 deploy-seamless.sh）
#
# 历史入口名保留（CI / 外部脚本可能引用），但实际逻辑已统一到
# deploy-seamless.sh deploy 154，与 deploy-245.sh 模式一致。
#
# 用法:
#   bash scripts/deploy-to-154-auto.sh                 # 部署 (build_seq +1)
#   bash scripts/deploy-to-154-auto.sh --seq 1027      # 指定 seq
#   bash scripts/deploy-to-154-auto.sh --no-frontend   # 仅后端
#
# 等价于:
#   bash scripts/deploy-seamless.sh deploy 154 [args...]
# =====================================================================
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
exec bash "$SCRIPT_DIR/deploy-seamless.sh" deploy 154 "$@"

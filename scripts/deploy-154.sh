#!/usr/bin/env bash
# =====================================================================
# scripts/deploy-154.sh — 154 生产无感部署（委托 deploy-seamless.sh）
#
# 154 是生产环境，默认通过 252 跳板机连接（最稳定）。
# 默认：前后端同时构建 + 切换前 DB 迁移 + 原子符号链接切换 + db-changelog。
#
# 用法:
#   bash scripts/deploy-154.sh                 # 无感部署（build_seq +1，通过 252 跳板机）
#   bash scripts/deploy-154.sh --seq 1143      # 指定 seq
#   bash scripts/deploy-154.sh --no-frontend   # 仅后端（不推荐）
#   bash scripts/deploy-154.sh --direct        # 直连 154（跳过 252 跳板机，应急用）
#
# SSH 连接策略:
#   默认: 通过 252 跳板机 (root@115.29.212.252) → 154，避免公网 IP 抖动
#   --direct: 直连 47.97.111.154:25022（仅在 252 不可达时使用）
#
# 等价于:
#   bash scripts/deploy-seamless.sh deploy 154 [args...]
# =====================================================================
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
exec bash "$SCRIPT_DIR/deploy-seamless.sh" deploy 154 "$@"

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
#   bash scripts/deploy-154.sh --force         # 恢复并重建 154 的锁，再部署
#   bash scripts/deploy-154.sh --force-unlock # --force 的兼容别名
#   两种参数都会在 seamless 中恢复 stale locks 后重新建锁。
#
# --force / --force-unlock 会在 seamless 流程中恢复并重建所有相关锁。
# 这会在确认持有者属于部署进程后终止本机残留进程，并清理目标机残留锁；
# 仅在你确认旧 deploy 不应继续运行时使用。
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

# shellcheck source=deploy-lib/parse-wrapper-flags.sh
source "$SCRIPT_DIR/deploy-lib/parse-wrapper-flags.sh"

# Strip operator-level recovery flags before delegating. The seamless
# orchestrator owns recovery of all lock layers and then reacquires them.
FORCE_UNLOCK=0
ARGS=()
extract_force_unlock FORCE_UNLOCK ARGS "$@"

if [[ $FORCE_UNLOCK -eq 1 ]]; then
  ARGS+=(--force)
fi

exec bash "$SCRIPT_DIR/deploy-seamless.sh" deploy 154 "${ARGS[@]}"

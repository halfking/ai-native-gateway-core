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
#   bash scripts/deploy-154.sh --force-unlock  # 先清理 stale 的本地 repo 锁，再部署
#
# --force-unlock 会调用 scripts/deploy-lib/unlock-local.sh --force。
# 默认该脚本只会报告不会删；加 --force 才真正移除本地锁并 (必要时) 杀掉
# 还活着的持有者 PID。仅在你确认旧 deploy 进程已死/不该再跑时使用。
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

# Parse --force-unlock before exec. The wrapper is intentionally tiny,
# so we only pre-handle the one flag that must run before delegating;
# every other flag passes through to deploy-seamless.sh unchanged.
FORCE_UNLOCK=0
ARGS=()
for arg in "$@"; do
  case "$arg" in
    --force-unlock) FORCE_UNLOCK=1 ;;
    *)              ARGS+=("$arg") ;;
  esac
done

if [[ $FORCE_UNLOCK -eq 1 ]]; then
  echo "[deploy-154] --force-unlock: running unlock-local.sh --force"
  bash "$SCRIPT_DIR/deploy-lib/unlock-local.sh" --force
fi

exec bash "$SCRIPT_DIR/deploy-seamless.sh" deploy 154 "${ARGS[@]}"

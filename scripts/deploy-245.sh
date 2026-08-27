#!/usr/bin/env bash
# =====================================================================
# scripts/deploy-245.sh — 245 预发布无感部署（委托 deploy-seamless.sh）
#
# 245 是 154 生产环境的预发布验证环节。
# 默认：前后端同时构建 + 切换前 DB 迁移 + 原子符号链接切换 + db-changelog。
#
# 用法:
#   bash scripts/deploy-245.sh                 # 无感部署（build_seq +1）
#   bash scripts/deploy-245.sh --seq 1027      # 指定 seq
#   bash scripts/deploy-245.sh --no-frontend   # 仅后端（不推荐）
#   bash scripts/deploy-245.sh --force-unlock  # 先清理 stale 的本地 repo 锁，再部署
#
# --force-unlock 会调用 scripts/deploy-lib/unlock-local.sh --force。
# 默认该脚本只会报告不会删；加 --force 才真正移除本地锁并 (必要时) 杀掉
# 还活着的持有者 PID。仅在你确认旧 deploy 进程已死/不该再跑时使用。
#
# 等价于:
#   bash scripts/deploy-seamless.sh deploy 245 [args...]
# =====================================================================
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

FORCE_UNLOCK=0
ARGS=()
for arg in "$@"; do
  case "$arg" in
    --force-unlock) FORCE_UNLOCK=1 ;;
    *)              ARGS+=("$arg") ;;
  esac
done

if [[ $FORCE_UNLOCK -eq 1 ]]; then
  echo "[deploy-245] --force-unlock: running unlock-local.sh --force"
  bash "$SCRIPT_DIR/deploy-lib/unlock-local.sh" --force
fi

exec bash "$SCRIPT_DIR/deploy-seamless.sh" deploy 245 "${ARGS[@]}"

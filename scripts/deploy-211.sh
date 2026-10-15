#!/usr/bin/env bash
# =====================================================================
# scripts/deploy-211.sh — 211 docker 专职机部署（委托 deploy-211-docker.sh）
#
# 与 deploy-154.sh / deploy-245.sh 同一族入口标准：本文件只做目标机语境
# 的包装（flag 解析、--dry-run 合同、探针超时默认），部署逻辑全部在引擎
# 脚本。211 的引擎是 deploy-211-docker.sh（docker 形态：tar 同步 → 本地
# 离线基础镜像构建 → 容器重建 → 复用 acc-pg17/nbjl-redis → 迁移重放 →
# web 前端种子）。凭据/Redis 库号/版本元数据已持久化在 211，无需本机传。
#
# 用法:
#   bash scripts/deploy-211.sh                 # 部署最新代码到 211
#   bash scripts/deploy-211.sh --redis-db 9    # 引擎参数原样转发
#   bash scripts/deploy-211.sh --dry-run       # 打印目标合同 JSON
#   bash scripts/deploy-211.sh --force         # 211 部署无部署锁：接受但无操作
#
# 前置: web/dist 与 web-mobile/dist 由部署机预构建（pnpm build），
# 引擎随 tar 上树并种子进容器；缺失时引擎 warn 且 root 回落版本 JSON。
#
# 等价于:
#   bash scripts/deploy-211-docker.sh --via 192.168.31.211 [args...]
# =====================================================================
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# Password authentication is never used by the canonical deploy path. Clear a
# stale inherited value so unrelated shell configuration cannot block a key-only
# deployment or leak into child processes.
unset SSHPASS

# scripts/deploy-lib is a repo-relative symlink that dangles in temporary
# worktrees (/private/tmp/lgw-merge-*); resolve the SSOT robustly instead of
# sourcing through it (env preset → link target → $HOME default, same order
# as _shared-lib.sh; 2026-10-07 audit round F2).
# shellcheck source=deploy-lib-resolve.sh
source "$SCRIPT_DIR/deploy-lib-resolve.sh"
# shellcheck source=deploy-lib/parse-wrapper-flags.sh
source "$AIAN_DEPLOY_LIB/parse-wrapper-flags.sh"

# Strip operator-level recovery flags before delegating. The seamless
# orchestrator owns recovery of all lock layers and then reacquires them.
FORCE_UNLOCK=0
ARGS=()
extract_force_unlock FORCE_UNLOCK ARGS "$@"

# bash 3.2 + set -u: empty-array "${ARGS[@]}" / "${ARGS[*]}" expansions die as
# unbound — guard by count (no-arg `bash scripts/deploy-211.sh` path).
if (( ${#ARGS[@]} > 0 )); then
  for arg in "${ARGS[@]}"; do
    [[ "$arg" == --help || "$arg" == -h ]] && { sed -n '2,22p' "$0"; exit 0; }
  done
  if [[ " ${ARGS[*]} " == *' --dry-run '* ]]; then
    # 211 是单容器 docker 部署，无蓝绿：active_port 固定 8781
    # （引擎 D211_GW_PORT 默认），candidate_port 不适用（null）。
    printf '{"target":"211","active_port":"8781","candidate_port":null}\n'
    exit 0
  fi
fi

if [[ $FORCE_UNLOCK -eq 1 ]]; then
  # 家族口径里 --force = 恢复并重建部署锁；211 引擎无部署锁（每次部署整体
  # rm -rf 重建目标树），卡死场景直接重跑即可，故接受但无操作，明说不静默。
  echo "[deploy-211] 211 docker 部署无部署锁，--force 无需也无效（忽略）" >&2
fi

# 透传健康探针超时：与 deploy-154/245 同一族标准（PROBE_TIMEOUT_SECS）。
# 211 的就绪等待是引擎内 curl 轮询（容器首启 ensure 链 + 前端种子挂载，
# 默认 120s），调用方可覆盖，引擎按 2s 步进取半作为轮次上限。
export PROBE_TIMEOUT_SECS="${PROBE_TIMEOUT_SECS:-120}"

D211_TARGET="${D211_TARGET_IP:-192.168.31.211}"
# bash 3.2 + set -u: the `${ARGS[@]}+${ARGS[@]}` guard keeps the no-arg path
# (empty ARGS) from dying as an unbound variable, same reason as the guard above.
exec bash "$SCRIPT_DIR/deploy-211-docker.sh" --via "$D211_TARGET" "${ARGS[@]+"${ARGS[@]}"}"

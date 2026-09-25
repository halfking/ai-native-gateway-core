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

# Password authentication is never used by the canonical deploy path. Clear a
# stale inherited value so unrelated shell configuration cannot block a key-only
# deployment or leak into child processes.
unset SSHPASS

# shellcheck source=deploy-lib/parse-wrapper-flags.sh
source "$SCRIPT_DIR/deploy-lib/parse-wrapper-flags.sh"

# Strip operator-level recovery flags before delegating. The seamless
# orchestrator owns recovery of all lock layers and then reacquires them.
FORCE_UNLOCK=0
ARGS=()
extract_force_unlock FORCE_UNLOCK ARGS "$@"

# bash 3.2 + set -u: empty-array "${ARGS[@]}" / "${ARGS[*]}" expansions die as
# unbound — guard by count (no-arg `bash scripts/deploy-154.sh` path).
if (( ${#ARGS[@]} > 0 )); then
  for arg in "${ARGS[@]}"; do
    [[ "$arg" == --help || "$arg" == -h ]] && { sed -n '2,27p' "$0"; exit 0; }
  done
  if [[ " ${ARGS[*]} " == *' --dry-run '* ]]; then
    # Port literals MUST mirror targets.sh:154 (active_port "8781", candidate_port "8782");
    # deploy-seamless.sh reads the same contract via target_field at deploy time.
    printf '{"target":"154","active_port":"8781","candidate_port":"8782"}\n'
    exit 0
  fi
fi

if [[ $FORCE_UNLOCK -eq 1 ]]; then
  ARGS+=(--force)
fi

# 透传健康探针超时：候选首跑 ensure 链 + 列交集检查在大表上常超 60s
# （154 与 245 共享 252 PG，冷启动 ensure 链同量级，90-150s 区间有两次实测）。
# exec bash 会重启子 shell，不继承调用方未 export 的环境变量，所以显式 export 一次。
# 2026-09-20（可靠性）: 默认 180 -> 120s，与 deploy-245.sh 同步。
# 2026-09-26（owner 决策①）: 默认 120 -> 600s，对齐 245 的 d5eeb71eb —— 154 冷启动
# 暴露在压垮过 245 的同一失败模式下（共享 252 PG，ensure 链 90-150s），不再依赖
# 人工 PROBE_TIMEOUT_SECS env 救场。真坏候选的代价由"探针期不接流量 +
# 第二窗口 PROBE_RETRY_TIMEOUT_SECS=60s"兜底，与 245 现状一致。
export PROBE_TIMEOUT_SECS="${PROBE_TIMEOUT_SECS:-600}"

exec bash "$SCRIPT_DIR/deploy-seamless.sh" deploy 154 "${ARGS[@]}"

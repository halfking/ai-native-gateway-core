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
#   bash scripts/deploy-245.sh --force         # 恢复并重建 245 的锁，再部署
#   bash scripts/deploy-245.sh --force-unlock # --force 的兼容别名
#   两种参数都会在 seamless 中恢复 stale locks 后重新建锁。
#
# --force / --force-unlock 会在 seamless 流程中恢复并重建所有相关锁。
# 这会在确认持有者属于部署进程后终止本机残留进程，并清理目标机残留锁；
# 仅在你确认旧 deploy 不应继续运行时使用。
#
# 等价于:
#   bash scripts/deploy-seamless.sh deploy 245 [args...]
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
# unbound — guard by count (no-arg `bash scripts/deploy-245.sh` path).
if (( ${#ARGS[@]} > 0 )); then
  for arg in "${ARGS[@]}"; do
    [[ "$arg" == --help || "$arg" == -h ]] && { sed -n '2,22p' "$0"; exit 0; }
  done
  if [[ " ${ARGS[*]} " == *' --dry-run '* ]]; then
    # Port literals MUST mirror targets.sh:245 (active_port "8781", candidate_port "8782");
    # deploy-seamless.sh reads the same contract via target_field at deploy time.
    printf '{"target":"245","active_port":"8781","candidate_port":"8782"}\n'
    exit 0
  fi
fi

if [[ $FORCE_UNLOCK -eq 1 ]]; then
  ARGS+=(--force)
fi

# 透传健康探针超时：候选首跑 ensure 链 + 列交集检查在大表上常超 60s。
# exec bash 会重启子 shell，不继承调用方未 export 的环境变量，所以显式 export 一次。
# 2026-09-20（可靠性）: 默认 180 -> 120s。120s 仍给典型 ensure 链（60-90s 区间）
# 留 ~30s 余量；任何怀疑 ensure 真正超过 120s 的部署，用 PROBE_TIMEOUT_SECS=180
# （或更高）覆盖即可，env var 透传链仍生效。如需完全退回到旧行为（180s 默认），
# 加 PROBE_TIMEOUT_SECS=180 重跑 —— deploy-seamless.sh 内部也会随之回到 180s。
export PROBE_TIMEOUT_SECS="${PROBE_TIMEOUT_SECS:-120}"

exec bash "$SCRIPT_DIR/deploy-seamless.sh" deploy 245 "${ARGS[@]}"

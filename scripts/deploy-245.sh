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

if [[ -n "${SSHPASS:-}" ]]; then
  printf 'error: SSHPASS is forbidden; unset SSHPASS and run: eval "$(env-injector inject --target=aliyun-frontend-245)"\n' >&2
  printf 'hint: deployment requires the injected SSH_KEY_245 file\n' >&2
  exit 64
fi

# shellcheck source=deploy-lib/parse-wrapper-flags.sh
source "$SCRIPT_DIR/deploy-lib/parse-wrapper-flags.sh"

# Strip operator-level recovery flags before delegating. The seamless
# orchestrator owns recovery of all lock layers and then reacquires them.
FORCE_UNLOCK=0
ARGS=()
extract_force_unlock FORCE_UNLOCK ARGS "$@"

for arg in "${ARGS[@]}"; do
  [[ "$arg" == --help || "$arg" == -h ]] && { sed -n '2,22p' "$0"; exit 0; }
done
if [[ " ${ARGS[*]} " == *' --dry-run '* ]]; then
  printf '{"target":"245","active_port":"8782","candidate_port":"8781"}\n'
  exit 0
fi

if [[ $FORCE_UNLOCK -eq 1 ]]; then
  ARGS+=(--force)
fi

exec bash "$SCRIPT_DIR/deploy-seamless.sh" deploy 245 "${ARGS[@]}"

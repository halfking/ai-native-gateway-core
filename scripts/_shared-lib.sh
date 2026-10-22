#!/usr/bin/env bash
# llm-gateway-go 项目侧共享部署库入口（P1.1，UNIFICATION-PLAN-2026-09-09 §4）。
# 职责：导出 AIAN_DEPLOY_LIB 并预载 SSOT 必备（prereqs + 镜像解析）。
# 历史 scripts/deploy-lib/ 副本已移至 deploy-lib.legacy/ 留档（P2 完成后删）；
# scripts/deploy-lib 现为指向 ~/workspace/ai-native-workspace/deploy-lib/ 的相对软链，
# 供 deploy-154/245 等旧入口与 tests/ 继续按原路径加载。
#
# 2026-10-10：默认 SSOT 路径随软链一起从 ai-native-tools/ 迁到 ai-native-workspace/
# （commit 22e5e3f8f 只迁了软链，这个 env 默认值当时漏改）。本机 ai-native-tools/
# 已不存在，deploy-local.sh 在此直接以 exit 64 退出，且错误信息指向一个死路径。
# 现在两个候选路径都试，并保持「显式 AIAN_DEPLOY_LIB 优先」的既有语义。

_resolve_aian_deploy_lib() {
  # 显式指定优先：CI / 多仓共用 SSOT 时由调用方决定。
  if [[ -n "${AIAN_DEPLOY_LIB:-}" ]]; then printf '%s\n' "$AIAN_DEPLOY_LIB"; return 0; fi
  local candidate
  # 顺序与 scripts/deploy-lib 软链一致：先 ai-native-workspace，再兼容旧布局。
  for candidate in \
    "$HOME/workspace/ai-native-workspace/deploy-lib" \
    "$HOME/workspace/ai-native-tools/deploy-lib"; do
    [[ -d "$candidate" ]] && { printf '%s\n' "$candidate"; return 0; }
  done
  return 1
}

if ! AIAN_DEPLOY_LIB="$(_resolve_aian_deploy_lib)"; then
  echo "missing AIAN_DEPLOY_LIB: no SSOT found under \$HOME/workspace/" >&2
  echo "fix: clone/create one of:" >&2
  echo "  ~/workspace/ai-native-workspace/deploy-lib   (current layout)" >&2
  echo "  ~/workspace/ai-native-tools/deploy-lib       (legacy layout)" >&2
  echo "  (SSOT created 2026-09-09 per docs/deployment/UNIFICATION-PLAN-2026-09-09.md §3.1)" >&2
  echo "  or set AIAN_DEPLOY_LIB explicitly" >&2
  return 64 2>/dev/null || exit 64
fi
export AIAN_DEPLOY_LIB

# 关键 SSOT 自动加载；其余文件按需 source。
# shellcheck source=deploy-prereqs.sh
source "$AIAN_DEPLOY_LIB/deploy-prereqs.sh"
# shellcheck source=deploy-image-resolution.sh
source "$AIAN_DEPLOY_LIB/deploy-image-resolution.sh"

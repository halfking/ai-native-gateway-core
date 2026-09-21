#!/usr/bin/env bash
# llm-gateway-go-5 项目侧共享部署库入口（P1.1，UNIFICATION-PLAN-2026-09-09 §4）。
# 职责：导出 AIAN_DEPLOY_LIB 并预载 SSOT 必备（prereqs + 镜像解析）。
# 历史 scripts/deploy-lib/ 副本已移至 deploy-lib.legacy/ 留档（P2 完成后删）；
# scripts/deploy-lib 现为指向 ~/workspace/ai-native-tools/deploy-lib/ 的相对软链，
# 供 deploy-154/245 等旧入口与 tests/ 继续按原路径加载。

export AIAN_DEPLOY_LIB="${AIAN_DEPLOY_LIB:-$HOME/workspace/ai-native-tools/deploy-lib}"
if [[ ! -d "$AIAN_DEPLOY_LIB" ]]; then
  echo "missing AIAN_DEPLOY_LIB: $AIAN_DEPLOY_LIB" >&2
  echo "fix: ensure ~/workspace/ai-native-tools/deploy-lib/ exists" >&2
  echo "  (SSOT created 2026-09-09 per docs/deployment/UNIFICATION-PLAN-2026-09-09.md §3.1)" >&2
  return 64 2>/dev/null || exit 64
fi

# 关键 SSOT 自动加载；其余文件按需 source。
# shellcheck source=deploy-prereqs.sh
source "$AIAN_DEPLOY_LIB/deploy-prereqs.sh"
# shellcheck source=deploy-image-resolution.sh
source "$AIAN_DEPLOY_LIB/deploy-image-resolution.sh"

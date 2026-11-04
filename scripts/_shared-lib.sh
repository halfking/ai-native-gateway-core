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
#
# ★ 2026-11-04 审计轮（F-15）：★ 解析器与失败语义对齐 —— 本文件此前**不校验**
#   显式 AIAN_DEPLOY_LIB 是否指向一个**可用的** SSOT ⇒ 显式值无效时解析阶段照样
#   「成功」⇒ 直到下面 source prereqs 才炸出 `No such file or directory`（rc=1），
#   而兄弟解析器 scripts/deploy-lib-resolve.sh 对**同一个**输入给的是
#   `missing deploy-lib SSOT: …` + `fix: …` + **rc=64**
#   ⇒ ⇒ 两条解析路径对同一故障给出**不同的错误语义** ⇒ 本文件按兄弟口径对齐。
# ★ 另：★ 中止语义不再依赖调用方自己有 `set -e`（详见失败分支的注释）。

# ★ 「一个可用的 SSOT 目录」的判据：★ 本文件接下来要 source 的两个文件都在。
#   ★ 刻意**不用** parse-wrapper-flags.sh 作判据 —— 那是兄弟解析器的口径；
#   ★ 本文件真正需要的是下面这两个 ⇒ 判据应当贴合**自己的**前置条件。
_shared_lib_usable() {
  [[ -n "${1:-}" && -f "$1/deploy-prereqs.sh" && -f "$1/deploy-image-resolution.sh" ]]
}

_resolve_aian_deploy_lib() {
  # 显式指定优先：CI / 多仓共用 SSOT 时由调用方决定。
  # ★ F-15：★ 显式值也要**校验** ⇒ 无效时返回 2（与「没找到候选」的 1 区分开），
  #   ★ 这样错误提示能说清「是你指错了」而不是「机器上没有」。
  if [[ -n "${AIAN_DEPLOY_LIB:-}" ]]; then
    _shared_lib_usable "$AIAN_DEPLOY_LIB" || return 2
    printf '%s\n' "$AIAN_DEPLOY_LIB"; return 0
  fi
  local candidate
  # 顺序与 scripts/deploy-lib 软链一致：先 ai-native-workspace，再兼容旧布局。
  for candidate in \
    "$HOME/workspace/ai-native-workspace/deploy-lib" \
    "$HOME/workspace/ai-native-tools/deploy-lib"; do
    if _shared_lib_usable "$candidate"; then printf '%s\n' "$candidate"; return 0; fi
  done
  return 1
}

# ★ 解析失败时的中止语义 —— ★ 与「可执行提示 + rc=64」一起构成契约。
#   ★ 旧写法 `return 64 2>/dev/null || exit 64` 在**被 source** 时只从本文件返回，
#     ⇒ 是否中止**取决于调用方自己有没有 set -e** ⇒ 没有 set -e 的调用方会带着
#     空的 AIAN_DEPLOY_LIB 继续跑 ⇒ 报出下游的 `/lock.sh: No such file or
#     directory` 噪声而不是可执行提示。
#   ★ 现在：★ 自动化调用方（脚本）一律 exit 64 ⇒ 不再依赖调用方的 set -e；
#     ★ 交互式 shell 保留 return（不杀掉用户的会话）⇒ 只把 64 交回调用方。
#   ★ 注意：★ return 只能写在**顶层**（函数里的 return 只返回那个函数）⇒ 本段
#     刻意不用函数包裹。
# ★ 记下调用方给的显式值：★ 解析失败时 AIAN_DEPLOY_LIB 已被赋成解析器的空输出，
#   ★ 不留底就报不出「到底是哪个值指错了」。
_shared_lib_explicit="${AIAN_DEPLOY_LIB:-}"
_shared_lib_resolve_rc=0
AIAN_DEPLOY_LIB="$(_resolve_aian_deploy_lib)" || _shared_lib_resolve_rc=$?
if [[ "$_shared_lib_resolve_rc" -ne 0 ]]; then
  if [[ "$_shared_lib_resolve_rc" -eq 2 ]]; then
    echo "missing deploy-lib SSOT: $_shared_lib_explicit (deploy-prereqs.sh / deploy-image-resolution.sh not found)" >&2
    echo "fix: export AIAN_DEPLOY_LIB=/path/to/deploy-lib-checkout" >&2
  else
    echo "missing AIAN_DEPLOY_LIB: no SSOT found under \$HOME/workspace/" >&2
    echo "fix: clone/create one of:" >&2
    echo "  ~/workspace/ai-native-workspace/deploy-lib   (current layout)" >&2
    echo "  ~/workspace/ai-native-tools/deploy-lib       (legacy layout)" >&2
    echo "  (SSOT created 2026-09-09 per docs/deployment/UNIFICATION-PLAN-2026-09-09.md §3.1)" >&2
    echo "  or set AIAN_DEPLOY_LIB explicitly" >&2
  fi
  if [[ "${BASH_SOURCE[0]}" == "${0}" ]]; then
    exit 64
  fi
  if [[ "$-" == *i* ]]; then
    return 64
  fi
  exit 64
fi
export AIAN_DEPLOY_LIB

# 关键 SSOT 自动加载；其余文件按需 source。
# shellcheck source=deploy-prereqs.sh
source "$AIAN_DEPLOY_LIB/deploy-prereqs.sh"
# shellcheck source=deploy-image-resolution.sh
source "$AIAN_DEPLOY_LIB/deploy-image-resolution.sh"
